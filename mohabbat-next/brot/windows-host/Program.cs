using System;
using System.IO;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Text;
using ZstdSharp;

internal static class Program
{
    private const uint MemCommit = 0x1000;
    private const uint MemReserve = 0x2000;
    private const uint MemRelease = 0x8000;
    private const uint PageExecuteReadWrite = 0x40;
    private const uint ConsoleCodePageUtf8 = 65001;
    private static readonly int PrefixAbiOffset = 0x20000;
    private const ushort ProcessorArchitectureAmd64 = 9;
    private const ushort ProcessorArchitectureArm64 = 12;
    private const int MaximumComponentSize = 128 * 1024 * 1024;
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
        public IntPtr RuntimeSlice;
        public UIntPtr RuntimeSliceSize;
        public IntPtr Argv;
        public UIntPtr Argc;
        public IntPtr WasmPayload;
        public UIntPtr WasmPayloadSize;
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

    [System.Runtime.CompilerServices.MethodImpl(System.Runtime.CompilerServices.MethodImplOptions.NoInlining)]
    private static unsafe void PrecommitStack()
    {
        const int bytes = 512 * 1024;
        byte* p = stackalloc byte[bytes];
        for (int i = 0; i < bytes; i += 4096)
        {
            p[i] = 0;
        }
    }

    private static int Main(string[] args)
    {
        AppDomain.CurrentDomain.AssemblyResolve += ResolveEmbeddedAssembly;

        IntPtr platformAlloc = IntPtr.Zero;
        IntPtr argumentVector = IntPtr.Zero;
        UIntPtr argumentCount = UIntPtr.Zero;
        GCHandle platformPin = new GCHandle();
        GCHandle runtimePin = new GCHandle();
        GCHandle wasmPin = new GCHandle();
        LoadLibraryCallback loadLibrary = LoadLibrary;
        GetProcAddressCallback getProcAddress = GetProcAddress;
        AllocCallback alloc = Allocate;
        FreeCallback free = Free;
        uint previousInputCodePage = GetConsoleCP();
        uint previousOutputCodePage = GetConsoleOutputCP();
        if (previousInputCodePage != 0)
        {
            if (!SetConsoleCP(ConsoleCodePageUtf8))
            {
                WriteLastError("SetConsoleCP(UTF-8)");
            }
        }
        if (previousOutputCodePage != 0)
        {
            if (!SetConsoleOutputCP(ConsoleCodePageUtf8))
            {
                WriteLastError("SetConsoleOutputCP(UTF-8)");
            }
        }

        try
        {
            string[] rawCmd = Environment.GetCommandLineArgs();
            string[] guestArgs = new string[args.Length + 1];
            guestArgs[0] = rawCmd.Length > 0 ? rawCmd[0] : AppDomain.CurrentDomain.FriendlyName;
            Array.Copy(args, 0, guestArgs, 1, args.Length);
            argumentVector = AllocateArgumentVector(guestArgs, out argumentCount);

            string architecture = GetArchitecture();
            byte[] platform = ReadComponent("platform-windows-" + architecture);
            byte[] runtime = ReadComponent("runtime-" + architecture);
            byte[] wasm = ReadComponent("brain.wasm");

            platformPin = GCHandle.Alloc(platform, GCHandleType.Pinned);
            runtimePin = GCHandle.Alloc(runtime, GCHandleType.Pinned);
            wasmPin = GCHandle.Alloc(wasm, GCHandleType.Pinned);
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
                RuntimeSlice = runtimePin.AddrOfPinnedObject(),
                RuntimeSliceSize = new UIntPtr((uint)runtime.Length),
                Argv = argumentVector,
                Argc = argumentCount,
                WasmPayload = wasmPin.AddrOfPinnedObject(),
                WasmPayloadSize = new UIntPtr((uint)wasm.Length),
            };
            Marshal.StructureToPtr(host, platformAlloc, false);

            PlatformEntry entry =
                Marshal.GetDelegateForFunctionPointer<PlatformEntry>(platformCode);
            int status = 0;
            var worker = new System.Threading.Thread(() =>
            {
                PrecommitStack();
                status = entry();
            }, 16 * 1024 * 1024);
            worker.Start();
            worker.Join();
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
        catch (UnauthorizedAccessException exception)
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
            if (argumentVector != IntPtr.Zero)
            {
                Marshal.FreeHGlobal(argumentVector);
            }
            if (platformPin.IsAllocated)
            {
                platformPin.Free();
            }
            if (runtimePin.IsAllocated)
            {
                runtimePin.Free();
            }
            if (wasmPin.IsAllocated)
            {
                wasmPin.Free();
            }
            if (previousInputCodePage != 0)
            {
                if (!SetConsoleCP(previousInputCodePage))
                {
                    WriteLastError("restore SetConsoleCP");
                }
            }
            if (previousOutputCodePage != 0)
            {
                if (!SetConsoleOutputCP(previousOutputCodePage))
                {
                    WriteLastError("restore SetConsoleOutputCP");
                }
            }
            GC.KeepAlive(loadLibrary);
            GC.KeepAlive(getProcAddress);
            GC.KeepAlive(alloc);
            GC.KeepAlive(free);
        }
    }

    private static IntPtr AllocateArgumentVector(string[] args, out UIntPtr count)
    {
        count = new UIntPtr((uint)args.Length);
        if (args.Length == 0)
        {
            return IntPtr.Zero;
        }

        byte[][] encoded = new byte[args.Length][];
        int dataLength = 0;
        for (int i = 0; i < args.Length; i++)
        {
            encoded[i] = Encoding.UTF8.GetBytes(args[i]);
            if (Array.IndexOf(encoded[i], (byte)0) >= 0)
            {
                throw new InvalidOperationException("Command-line argument contains a NUL byte.");
            }
            dataLength = checked(dataLength + encoded[i].Length + 1);
        }

        int pointerBytes = checked(args.Length * IntPtr.Size);
        int totalBytes = checked(pointerBytes + dataLength);
        IntPtr block = Marshal.AllocHGlobal(totalBytes);
        bool complete = false;
        try
        {
            int dataOffset = pointerBytes;
            for (int i = 0; i < encoded.Length; i++)
            {
                IntPtr argument = IntPtr.Add(block, dataOffset);
                Marshal.WriteIntPtr(block, i * IntPtr.Size, argument);
                Marshal.Copy(encoded[i], 0, argument, encoded[i].Length);
                Marshal.WriteByte(argument, encoded[i].Length, 0);
                dataOffset += encoded[i].Length + 1;
            }
            complete = true;
            return block;
        }
        finally
        {
            if (!complete)
            {
                Marshal.FreeHGlobal(block);
            }
        }
    }

    private static byte[] s_sharedPool;

    private static byte[] LoadSharedPool()
    {
        if (s_sharedPool != null)
        {
            return s_sharedPool;
        }
        string exePath = typeof(Program).Assembly.Location;
        if (string.IsNullOrEmpty(exePath) || !File.Exists(exePath))
        {
            throw new InvalidOperationException("Cannot locate executable to read shared compressed pool.");
        }
        using (var fs = new FileStream(exePath, FileMode.Open, FileAccess.Read, FileShare.Read))
        {
            if (fs.Length < 24)
            {
                throw new InvalidOperationException("Executable is too small to contain shared pool trailer.");
            }
            fs.Seek(-24, SeekOrigin.End);
            byte[] trailer = new byte[24];
            int trRead = 0;
            while (trRead < 24)
            {
                int n = fs.Read(trailer, trRead, 24 - trRead);
                if (n <= 0) break;
                trRead += n;
            }
            if (trRead != 24 || Encoding.ASCII.GetString(trailer, 16, 8) != "MHBTTAIL")
            {
                throw new InvalidOperationException("Shared pool trailer (MHBTTAIL) not found.");
            }
            long poolOffset = BitConverter.ToInt64(trailer, 0);
            long poolLength = BitConverter.ToInt64(trailer, 8);
            if (poolOffset <= 0 || poolLength <= 0 || poolLength > MaximumComponentSize || poolOffset + poolLength > fs.Length - 24)
            {
                throw new InvalidOperationException("Shared pool range in trailer is invalid.");
            }
            fs.Seek(poolOffset, SeekOrigin.Begin);
            byte[] compressed = new byte[(int)poolLength];
            int total = 0;
            while (total < compressed.Length)
            {
                int n = fs.Read(compressed, total, compressed.Length - total);
                if (n <= 0)
                {
                    throw new InvalidOperationException("Unexpected EOF reading shared compressed pool.");
                }
                total += n;
            }
            byte[] pool = DecompressZstandard(compressed);
            if (pool.Length < 16 || Encoding.ASCII.GetString(pool, 0, 8) != "MHBTPOOL")
            {
                throw new InvalidOperationException("Decompressed shared pool has invalid magic.");
            }
            s_sharedPool = pool;
            return s_sharedPool;
        }
    }

    private static byte[] TryReadComponent(string name)
    {
        byte[] pool = LoadSharedPool();
        uint version = BitConverter.ToUInt32(pool, 8);
        uint count = BitConverter.ToUInt32(pool, 12);
        if (version != 1 || 16 + (long)count * 40 > pool.Length)
        {
            throw new InvalidOperationException("Shared pool header is invalid.");
        }
        for (uint i = 0; i < count; i++)
        {
            int tocOff = 16 + (int)i * 40;
            int nameLen = 0;
            while (nameLen < 32 && pool[tocOff + nameLen] != 0)
            {
                nameLen++;
            }
            string entryName = Encoding.UTF8.GetString(pool, tocOff, nameLen);
            if (entryName == name)
            {
                int off = (int)BitConverter.ToUInt32(pool, tocOff + 32);
                int len = (int)BitConverter.ToUInt32(pool, tocOff + 36);
                if (len <= 0 || off < 0 || (long)off + len > pool.Length)
                {
                    throw new InvalidOperationException("Shared pool entry has invalid bounds: " + name);
                }
                byte[] copy = new byte[len];
                Buffer.BlockCopy(pool, off, copy, 0, len);
                return copy;
            }
        }
        return null;
    }

    private static byte[] ReadComponent(string resourceName)
    {
        byte[] component = TryReadComponent(resourceName);
        if (component == null)
        {
            throw new InvalidOperationException(
                "Shared pool component is missing: " + resourceName);
        }
        if (component.Length == 0 || component.Length > MaximumComponentSize)
        {
            throw new InvalidOperationException(
                "Shared pool component has an invalid size: " + resourceName);
        }
        return component;
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
            WriteLastError("VirtualAlloc for runtime slice");
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
    private static extern uint GetConsoleCP();

    [DllImport("kernel32.dll")]
    private static extern uint GetConsoleOutputCP();

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool SetConsoleCP(uint codePage);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool SetConsoleOutputCP(uint codePage);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool FlushInstructionCache(
        IntPtr process,
        IntPtr address,
        UIntPtr size);

    [DllImport("kernel32.dll")]
    private static extern void GetSystemInfo(out SystemInfo systemInfo);

}
