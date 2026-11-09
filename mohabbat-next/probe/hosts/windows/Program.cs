using System;
using System.IO;
using System.Reflection;
using System.Runtime.InteropServices;
using ZstdSharp;

internal static class Program
{
    private const uint MemCommit = 0x1000;
    private const uint MemReserve = 0x2000;
    private const uint MemRelease = 0x8000;
    private const uint PageExecuteReadWrite = 0x40;
    private const int PrefixAbiOffset = 0x20000;
    private const ushort ProcessorArchitectureAmd64 = 9;
    private const ushort ProcessorArchitectureArm64 = 12;
    private const int MaximumComponentSize = 4096;
    private const string InternalResourcePrefix = "MohabbatNext.Internal.";

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate IntPtr LoadLibraryCallback(
        [MarshalAs(UnmanagedType.LPStr)] string name);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate IntPtr GetProcAddressCallback(
        IntPtr module,
        [MarshalAs(UnmanagedType.LPStr)] string name);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate IntPtr AllocCallback(UIntPtr size);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate void FreeCallback(IntPtr address);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate int PlatformEntry();

    [StructLayout(LayoutKind.Sequential)]
    private struct WindowsPlatformHost
    {
        public IntPtr LoadLibrary;
        public IntPtr GetProcAddress;
        public IntPtr Alloc;
        public IntPtr Free;
        public IntPtr HelloPayload;
        public UIntPtr HelloPayloadSize;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct SystemInfo
    {
        public ushort ProcessorArchitecture;
        public ushort Reserved;
        public uint PageSize;
        public IntPtr MinimumApplicationAddress;
        public IntPtr MaximumApplicationAddress;
        public UIntPtr ActiveProcessorMask;
        public uint NumberOfProcessors;
        public uint ProcessorType;
        public uint AllocationGranularity;
        public ushort ProcessorLevel;
        public ushort ProcessorRevision;
    }

    private static int Main(string[] args)
    {
        AppDomain.CurrentDomain.AssemblyResolve += ResolveEmbeddedAssembly;
        if (args.Length == 1 && args[0] == "--verify-components")
        {
            return VerifyComponents();
        }
        if (args.Length != 0)
        {
            Console.Error.WriteLine("This executable does not accept arguments.");
            return 2;
        }

        IntPtr platformAlloc = IntPtr.Zero;
        GCHandle platformPin = new GCHandle();
        GCHandle helloPin = new GCHandle();
        LoadLibraryCallback loadLibrary = LoadLibrary;
        GetProcAddressCallback getProcAddress = GetProcAddress;
        AllocCallback alloc = Allocate;
        FreeCallback free = Free;

        try
        {
            string architecture = GetArchitecture();
            byte[] platform = ReadComponent(
                string.Format(
                    "MohabbatNext.Platform.Windows.{0}.zst",
                    architecture));
            byte[] hello = ReadComponent(
                string.Format(
                    "MohabbatNext.Hello.{0}.zst",
                    architecture));

            platformPin = GCHandle.Alloc(platform, GCHandleType.Pinned);
            helloPin = GCHandle.Alloc(hello, GCHandleType.Pinned);
            int totalPlatformSize = PrefixAbiOffset + platform.Length;
            platformAlloc = VirtualAlloc(
                IntPtr.Zero,
                new UIntPtr((uint)totalPlatformSize),
                MemCommit | MemReserve,
                PageExecuteReadWrite);
            if (platformAlloc == IntPtr.Zero)
            {
                WriteLastError("VirtualAlloc for platform abstraction");
                return 1;
            }

            IntPtr platformCode = new IntPtr(platformAlloc.ToInt64() + PrefixAbiOffset);
            Marshal.Copy(platform, 0, platformCode, platform.Length);
            if (!FlushInstructionCache(
                    GetCurrentProcess(),
                    platformCode,
                    new UIntPtr((uint)platform.Length)))
            {
                WriteLastError("FlushInstructionCache for platform abstraction");
                return 1;
            }

            var host = new WindowsPlatformHost
            {
                LoadLibrary = Marshal.GetFunctionPointerForDelegate(loadLibrary),
                GetProcAddress =
                    Marshal.GetFunctionPointerForDelegate(getProcAddress),
                Alloc = Marshal.GetFunctionPointerForDelegate(alloc),
                Free = Marshal.GetFunctionPointerForDelegate(free),
                HelloPayload = helloPin.AddrOfPinnedObject(),
                HelloPayloadSize = new UIntPtr((uint)hello.Length),
            };
            Marshal.StructureToPtr(host, platformAlloc, false);

            PlatformEntry entry =
                Marshal.GetDelegateForFunctionPointer<PlatformEntry>(platformCode);
            int status = entry();
            Console.Error.WriteLine(
                "Windows platform abstraction returned unexpectedly (status {0}).",
                status);
            return status == 0 ? 70 : status;
        }
        catch (InvalidOperationException exception)
        {
            Console.Error.WriteLine(exception.Message);
            return 1;
        }
        catch (IOException exception)
        {
            Console.Error.WriteLine(exception.Message);
            return 1;
        }
        finally
        {
            if (platformAlloc != IntPtr.Zero)
            {
                VirtualFree(platformAlloc, UIntPtr.Zero, MemRelease);
            }
            if (platformPin.IsAllocated)
            {
                platformPin.Free();
            }
            if (helloPin.IsAllocated)
            {
                helloPin.Free();
            }
            GC.KeepAlive(loadLibrary);
            GC.KeepAlive(getProcAddress);
            GC.KeepAlive(alloc);
            GC.KeepAlive(free);
        }
    }

    private static int VerifyComponents()
    {
        string[] architectures = { "x86_64", "aarch64" };
        foreach (string architecture in architectures)
        {
            ReadComponent(
                "MohabbatNext.Platform.Windows." + architecture + ".zst");
            ReadComponent("MohabbatNext.Hello." + architecture + ".zst");
        }
        Console.WriteLine("Verified all four compressed components.");
        return 0;
    }

    private static byte[] ReadComponent(string resourceName)
    {
        Stream resource =
            typeof(Program).Assembly.GetManifestResourceStream(resourceName);
        if (resource == null)
        {
            throw new InvalidOperationException(
                "Embedded component is missing: " + resourceName);
        }

        using (resource)
        using (var buffer = new MemoryStream())
        {
            resource.CopyTo(buffer);
            byte[] component = DecompressZstandard(buffer.ToArray());
            if (component.Length == 0 ||
                component.Length > MaximumComponentSize)
            {
                throw new InvalidOperationException(
                    "Embedded component has an invalid size: " +
                    resourceName);
            }
            return component;
        }
    }

    private static byte[] DecompressZstandard(byte[] compressed)
    {
        using (var decompressor = new Decompressor())
        {
            return decompressor.Unwrap(compressed).ToArray();
        }
    }

    private static Assembly ResolveEmbeddedAssembly(
        object sender,
        ResolveEventArgs args)
    {
        string assemblyName = new AssemblyName(args.Name).Name;
        string resourceName =
            InternalResourcePrefix + assemblyName + ".dll";
        using (Stream resource =
            typeof(Program).Assembly.GetManifestResourceStream(resourceName))
        {
            if (resource == null)
            {
                return null;
            }
            using (var buffer = new MemoryStream())
            {
                resource.CopyTo(buffer);
                return Assembly.Load(buffer.ToArray());
            }
        }
    }

    private static string GetArchitecture()
    {
        if (!Environment.Is64BitProcess)
        {
            throw new InvalidOperationException(
                "A 64-bit Windows process is required.");
        }

        SystemInfo systemInfo;
        GetSystemInfo(out systemInfo);
        if (systemInfo.ProcessorArchitecture == ProcessorArchitectureAmd64)
        {
            return "x86_64";
        }
        if (systemInfo.ProcessorArchitecture == ProcessorArchitectureArm64)
        {
            return "aarch64";
        }
        throw new InvalidOperationException(
            string.Format(
                "Unsupported Windows process architecture: {0}.",
                systemInfo.ProcessorArchitecture));
    }

    private static IntPtr LoadLibrary(string name)
    {
        IntPtr module = LoadLibraryNative(name);
        if (module == IntPtr.Zero)
        {
            WriteLastError("LoadLibrary(" + name + ")");
        }
        return module;
    }

    private static IntPtr GetProcAddress(IntPtr module, string name)
    {
        IntPtr address = GetProcAddressNative(module, name);
        if (address == IntPtr.Zero)
        {
            WriteLastError("GetProcAddress(" + name + ")");
        }
        return address;
    }

    private static IntPtr Allocate(UIntPtr size)
    {
        IntPtr address = VirtualAlloc(
            IntPtr.Zero,
            size,
            MemCommit | MemReserve,
            PageExecuteReadWrite);
        if (address == IntPtr.Zero)
        {
            WriteLastError("VirtualAlloc for hello-world payload");
        }
        return address;
    }

    private static void Free(IntPtr address)
    {
        if (address != IntPtr.Zero &&
            !VirtualFree(address, UIntPtr.Zero, MemRelease))
        {
            WriteLastError("VirtualFree");
        }
    }

    private static void WriteLastError(string operation)
    {
        int error = Marshal.GetLastWin32Error();
        Console.Error.WriteLine(
            "{0} failed with Win32 error {1}.",
            operation,
            error);
    }

    [DllImport("kernel32.dll", EntryPoint = "LoadLibraryA", SetLastError = true)]
    private static extern IntPtr LoadLibraryNative(
        [MarshalAs(UnmanagedType.LPStr)] string name);

    [DllImport("kernel32.dll", EntryPoint = "GetProcAddress", SetLastError = true)]
    private static extern IntPtr GetProcAddressNative(
        IntPtr module,
        [MarshalAs(UnmanagedType.LPStr)] string name);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern IntPtr VirtualAlloc(
        IntPtr address,
        UIntPtr size,
        uint allocationType,
        uint protect);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool VirtualFree(
        IntPtr address,
        UIntPtr size,
        uint freeType);

    [DllImport("kernel32.dll")]
    private static extern IntPtr GetCurrentProcess();

    [DllImport("kernel32.dll")]
    private static extern IntPtr GetStdHandle(int standardHandle);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool FlushInstructionCache(
        IntPtr process,
        IntPtr address,
        UIntPtr size);

    [DllImport("kernel32.dll")]
    private static extern void GetSystemInfo(out SystemInfo systemInfo);
}
