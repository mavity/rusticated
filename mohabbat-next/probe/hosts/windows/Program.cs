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
        if (args.Length == 1 && args[0] == "--unit-test")
        {
            return RunUnitTests();
        }
        if (args.Length == 2 && args[0] == "--test-runtime")
        {
            return RunCraneliftRuntimeTest(args[1]);
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

    private static int RunUnitTests()
    {
        Console.WriteLine("RUN   [csharp/unit] WindowsPlatformHostLayout");
        int hostSize = Marshal.SizeOf(typeof(WindowsPlatformHost));
        if (hostSize != 48)
        {
            Console.Error.WriteLine("FAIL: Expected WindowsPlatformHost size 48, got " + hostSize);
            return 1;
        }
        if (PrefixAbiOffset != 0x20000)
        {
            Console.Error.WriteLine("FAIL: Expected PrefixAbiOffset 0x20000, got " + PrefixAbiOffset);
            return 1;
        }
        Console.WriteLine("PASS  [csharp/unit] WindowsPlatformHostLayout");

        Console.WriteLine("RUN   [csharp/unit] ArchitectureDetection");
        string arch = GetArchitecture();
        if (arch != "x86_64" && arch != "aarch64")
        {
            Console.Error.WriteLine("FAIL: Unexpected architecture: " + arch);
            return 1;
        }
        Console.WriteLine("PASS  [csharp/unit] ArchitectureDetection (" + arch + ")");

        Console.WriteLine("RUN   [csharp/unit] EmbeddedZstandardDecompression");
        if (VerifyComponents() != 0)
        {
            return 1;
        }
        Console.WriteLine("PASS  [csharp/unit] EmbeddedZstandardDecompression");
        return 0;
    }

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate IntPtr RuntimeAllocMemCallback(UIntPtr size, UIntPtr align, uint flags);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate int RuntimeProtectMemCallback(IntPtr ptr, UIntPtr size, uint flags);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate void RuntimeFreeMemCallback(IntPtr ptr, UIntPtr size);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate void RuntimeFlushIcacheCallback(IntPtr ptr, UIntPtr size);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate void RuntimeHostPanicCallback(IntPtr msgPtr, UIntPtr msgLen);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate void RuntimeSliceEntry();

    private static IntPtr RuntimeAllocMem(UIntPtr size, UIntPtr align, uint flags)
    {
        return VirtualAlloc(IntPtr.Zero, size, MemCommit | MemReserve, PageExecuteReadWrite);
    }

    private static int RuntimeProtectMem(IntPtr ptr, UIntPtr size, uint flags)
    {
        return 0;
    }

    private static void RuntimeFreeMem(IntPtr ptr, UIntPtr size)
    {
    }

    private static void RuntimeFlushIcache(IntPtr ptr, UIntPtr size)
    {
        FlushInstructionCache(GetCurrentProcess(), ptr, size);
    }

    private static void RuntimeHostPanic(IntPtr msgPtr, UIntPtr msgLen)
    {
        string msg = Marshal.PtrToStringAnsi(msgPtr, (int)msgLen.ToUInt64());
        Console.Error.WriteLine("Runtime slice panicked: " + msg);
        Environment.Exit(99);
    }

    private static int RunCraneliftRuntimeTest(string runtimeBinPath)
    {
        byte[] runtimeBytes = File.ReadAllBytes(runtimeBinPath);
        byte[] wasmBytes = new byte[] {
            0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
            0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
            0x03, 0x02, 0x01, 0x00,
            0x05, 0x03, 0x01, 0x00, 0x01,
            0x07, 0x07, 0x01, 0x03, (byte)'r', (byte)'u', (byte)'n', 0x00, 0x00,
            0x0a, 0x11, 0x01,
            0x0f, 0x00,
            0x41, 0x00,
            0x41, 0x00,
            0x28, 0x02, 0x00,
            0x41, 0x07,
            0x6a,
            0x36, 0x02, 0x00,
            0x0b
        };

        GCHandle wasmPin = GCHandle.Alloc(wasmBytes, GCHandleType.Pinned);
        RuntimeAllocMemCallback allocMem = RuntimeAllocMem;
        RuntimeProtectMemCallback protectMem = RuntimeProtectMem;
        RuntimeFreeMemCallback freeMem = RuntimeFreeMem;
        RuntimeFlushIcacheCallback flushIcache = RuntimeFlushIcache;
        RuntimeHostPanicCallback hostPanic = RuntimeHostPanic;

        int totalSize = PrefixAbiOffset + runtimeBytes.Length;
        IntPtr sliceAlloc = VirtualAlloc(
            IntPtr.Zero,
            new UIntPtr((uint)totalSize),
            MemCommit | MemReserve,
            PageExecuteReadWrite);
        if (sliceAlloc == IntPtr.Zero)
        {
            WriteLastError("VirtualAlloc for Cranelift runtime slice");
            return 1;
        }

        try
        {
            // Populate PalAbiTable at -0x20000 (sliceAlloc + 0)
            Marshal.WriteIntPtr(sliceAlloc, 0 * IntPtr.Size, wasmPin.AddrOfPinnedObject());
            Marshal.WriteIntPtr(sliceAlloc, 1 * IntPtr.Size, new IntPtr(wasmBytes.Length));
            Marshal.WriteIntPtr(sliceAlloc, 2 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(allocMem));
            Marshal.WriteIntPtr(sliceAlloc, 3 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(protectMem));
            Marshal.WriteIntPtr(sliceAlloc, 4 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(freeMem));
            Marshal.WriteIntPtr(sliceAlloc, 5 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(flushIcache));
            Marshal.WriteIntPtr(sliceAlloc, 6 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(hostPanic));

            IntPtr sliceCode = new IntPtr(sliceAlloc.ToInt64() + PrefixAbiOffset);
            Marshal.Copy(runtimeBytes, 0, sliceCode, runtimeBytes.Length);
            FlushInstructionCache(GetCurrentProcess(), sliceCode, new UIntPtr((uint)runtimeBytes.Length));

            RuntimeSliceEntry entry = Marshal.GetDelegateForFunctionPointer<RuntimeSliceEntry>(sliceCode);

            // Call #1 (Cold Entry: compiles WASM with Cranelift + runs "run" once -> mem[0] == 7)
            entry();

            // Read RuntimeGlobals at -0x10000 (sliceAlloc + 0x10000)
            IntPtr globalsPtr = new IntPtr(sliceAlloc.ToInt64() + 0x10000);
            byte initialized = Marshal.ReadByte(globalsPtr, 0);
            IntPtr guestMemBase = Marshal.ReadIntPtr(globalsPtr, IntPtr.Size);
            if (initialized != 1 || guestMemBase == IntPtr.Zero)
            {
                Console.Error.WriteLine("FAIL: RuntimeGlobals not initialized after Call #1");
                return 2;
            }
            int val1 = Marshal.ReadInt32(guestMemBase, 0);
            if (val1 != 7)
            {
                Console.Error.WriteLine("FAIL: Expected guest_mem[0] == 7 after Call #1, got " + val1);
                return 3;
            }

            // Call #2 & #3 (Warm Re-Triggers: immediately invokes compiled "run" -> mem[0] == 14, then 21)
            entry();
            int val2 = Marshal.ReadInt32(guestMemBase, 0);
            entry();
            int val3 = Marshal.ReadInt32(guestMemBase, 0);
            if (val2 != 14 || val3 != 21)
            {
                Console.Error.WriteLine("FAIL: Expected guest_mem[0] == 14 then 21, got " + val2 + " then " + val3);
                return 4;
            }

            Console.WriteLine(
                "PASS  [runtime/" + GetArchitecture() + "] Cold JIT + 2 Warm Re-Triggers verified (mem[0]: 7 -> 14 -> 21)");
            return 0;
        }
        finally
        {
            VirtualFree(sliceAlloc, UIntPtr.Zero, MemRelease);
            wasmPin.Free();
            GC.KeepAlive(allocMem);
            GC.KeepAlive(protectMem);
            GC.KeepAlive(freeMem);
            GC.KeepAlive(flushIcache);
            GC.KeepAlive(hostPanic);
        }
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
