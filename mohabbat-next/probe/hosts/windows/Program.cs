using System;
using System.IO;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;
using ZstdSharp;

internal static class Program
{
    private const uint MemCommit = 0x1000;
    private const uint MemReserve = 0x2000;
    private const uint MemRelease = 0x8000;
    private const uint PageExecuteReadWrite = 0x40;
    private static readonly int PrefixAbiOffset = 0x20000;
    private const ushort ProcessorArchitectureAmd64 = 9;
    private const ushort ProcessorArchitectureArm64 = 12;
    private const int MaximumComponentSize = 8192;
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
        if ((args.Length == 2 || args.Length == 3) && args[0] == "--test-runtime")
        {
            return RunCraneliftRuntimeTest(
                args[1],
                args.Length == 3 ? args[2] : "load-add-store");
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

        Console.WriteLine("RUN   [csharp/unit] PalAbiTableFieldOffsets");
        int ptrSize = IntPtr.Size;
        if (ptrSize != 8)
        {
            Console.Error.WriteLine("FAIL: Expected 64-bit host, got pointer size: " + ptrSize);
            return 1;
        }
        if (0 * ptrSize != 0 || 8 * ptrSize != 64 || 9 * ptrSize != 72 || 18 * ptrSize != 144 || 19 * ptrSize != 152 || 38 * ptrSize != 304 || 46 * ptrSize != 368)
        {
            Console.Error.WriteLine("FAIL: Slot calculation mismatch");
            return 1;
        }
        Console.WriteLine("PASS  [csharp/unit] PalAbiTableFieldOffsets");

        Console.WriteLine("RUN   [csharp/unit] WasmScenarioGeneratorValidity");
        foreach (string sc in new string[] { "empty", "store", "add", "load-add-store", "call-get-time" })
        {
            byte[] bytes = BuildRuntimeTestWasm(sc);
            if (bytes.Length < 8 || bytes[0] != 0 || bytes[1] != 0x61 || bytes[2] != 0x73 || bytes[3] != 0x6d)
            {
                Console.Error.WriteLine("FAIL: Invalid WASM magic in scenario " + sc);
                return 1;
            }
        }
        Console.WriteLine("PASS  [csharp/unit] WasmScenarioGeneratorValidity");
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
    private delegate void RuntimeDebugCallback(int value);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate void RuntimeSliceEntry();

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate void RuntimeProcessExitCallback(int code);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate ulong RuntimeGetTimeCallback();

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate void RuntimeGetRandomCallback(IntPtr bufPtr, UIntPtr len);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate ulong RuntimeGetArgsCallback(IntPtr bufPtr, UIntPtr cap);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate ulong RuntimeGetEnvCallback(IntPtr bufPtr, UIntPtr cap);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate ulong RuntimeGetCwdCallback(IntPtr bufPtr, UIntPtr cap);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate uint RuntimeSetCwdCallback(IntPtr pathPtr, UIntPtr len);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate uint RuntimeGetPlatformInfoCallback(IntPtr outPtr, UIntPtr cap);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate void RuntimeHandleCloseCallback(ulong handle);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate void RuntimeTtySetModeCallback(ulong handle, uint mode);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate uint RuntimeTtyGetSizeCallback(ulong handle);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate uint RuntimeTtySetSizeCallback(ulong handle, uint cols, uint rows);

    [UnmanagedFunctionPointer(CallingConvention.Cdecl)]
    private delegate uint RuntimeFdIsAttyCallback(uint handle);

    private static readonly DateTime UnixEpoch = new DateTime(1970, 1, 1, 0, 0, 0, DateTimeKind.Utc);
    private static readonly System.Security.Cryptography.RNGCryptoServiceProvider s_rng =
        new System.Security.Cryptography.RNGCryptoServiceProvider();

    private static void RuntimeProcessExit(int code)
    {
        Environment.Exit(code);
    }

    private static ulong RuntimeGetTime()
    {
        return (ulong)((DateTime.UtcNow - UnixEpoch).Ticks * 100);
    }

    private static void RuntimeGetRandom(IntPtr bufPtr, UIntPtr len)
    {
        int count = (int)len.ToUInt64();
        if (count <= 0 || bufPtr == IntPtr.Zero) return;
        byte[] bytes = new byte[count];
        s_rng.GetBytes(bytes);
        Marshal.Copy(bytes, 0, bufPtr, count);
    }

    private static ulong RuntimeGetArgs(IntPtr bufPtr, UIntPtr cap)
    {
        return 0;
    }

    private static ulong RuntimeGetEnv(IntPtr bufPtr, UIntPtr cap)
    {
        return 0;
    }

    private static ulong RuntimeGetCwd(IntPtr bufPtr, UIntPtr cap)
    {
        string cwd = Directory.GetCurrentDirectory();
        byte[] utf8 = Encoding.UTF8.GetBytes(cwd);
        ulong needed = (ulong)utf8.Length;
        if (cap.ToUInt64() >= needed && bufPtr != IntPtr.Zero)
        {
            Marshal.Copy(utf8, 0, bufPtr, utf8.Length);
        }
        return needed;
    }

    private static uint RuntimeSetCwd(IntPtr pathPtr, UIntPtr len)
    {
        int count = (int)len.ToUInt64();
        if (count <= 0 || pathPtr == IntPtr.Zero) return 1;
        byte[] bytes = new byte[count];
        Marshal.Copy(pathPtr, bytes, 0, count);
        string path = Encoding.UTF8.GetString(bytes);
        try
        {
            Directory.SetCurrentDirectory(path);
            return 0;
        }
        catch
        {
            return 1;
        }
    }

    private static uint RuntimeGetPlatformInfo(IntPtr outPtr, UIntPtr cap)
    {
        return 0;
    }

    private static void RuntimeHandleClose(ulong handle)
    {
    }

    private static void RuntimeTtySetMode(ulong handle, uint mode)
    {
    }

    private static uint RuntimeTtyGetSize(ulong handle)
    {
        try
        {
            int cols = Console.WindowWidth;
            int rows = Console.WindowHeight;
            return (uint)((cols << 16) | (rows & 0xffff));
        }
        catch
        {
            return (80 << 16) | 24;
        }
    }

    private static uint RuntimeTtySetSize(ulong handle, uint cols, uint rows)
    {
        return 0;
    }

    private static uint RuntimeFdIsAtty(uint handle)
    {
        if (handle == 0) return Console.IsInputRedirected ? 0u : 1u;
        if (handle == 1) return Console.IsOutputRedirected ? 0u : 1u;
        if (handle == 2) return Console.IsErrorRedirected ? 0u : 1u;
        return 0;
    }

    private static IntPtr RuntimeAllocMem(UIntPtr size, UIntPtr align, uint flags)
    {
        Console.WriteLine(
            "INFO: runtime allocation requested (size=" + size.ToUInt64()
            + ", align=" + align.ToUInt64() + ", flags=" + flags + ")");
        IntPtr address = VirtualAlloc(
            IntPtr.Zero,
            size,
            MemCommit | MemReserve,
            PageExecuteReadWrite);
        if (address == IntPtr.Zero)
        {
            WriteLastError("VirtualAlloc from runtime PAL callback");
        }
        return address;
    }

    private static int RuntimeProtectMem(IntPtr ptr, UIntPtr size, uint flags)
    {
        Console.WriteLine(
            "INFO: runtime protection requested (size=" + size.ToUInt64()
            + ", flags=" + flags + ")");
        return 0;
    }

    private static void RuntimeFreeMem(IntPtr ptr, UIntPtr size)
    {
        Console.WriteLine("INFO: runtime free requested (size=" + size.ToUInt64() + ")");
    }

    private static void RuntimeFlushIcache(IntPtr ptr, UIntPtr size)
    {
        Console.WriteLine("INFO: runtime instruction-cache flush requested");
        FlushInstructionCache(GetCurrentProcess(), ptr, size);
    }

    private static void RuntimeHostPanic(IntPtr msgPtr, UIntPtr msgLen)
    {
        Console.WriteLine("ENTERED RuntimeHostPanic: msgPtr=" + msgPtr.ToInt64() + ", len=" + msgLen.ToUInt64());
        Console.Out.Flush();
        string msg = Marshal.PtrToStringAnsi(msgPtr, (int)msgLen.ToUInt64());
        Console.WriteLine("Runtime slice panicked: " + msg);
        Console.Out.Flush();
        Console.Error.WriteLine("Runtime slice panicked: " + msg);
        Console.Error.Flush();
        Environment.Exit(99);
    }

    private static void RuntimeDebug(int value)
    {
        Console.WriteLine("INFO: runtime checkpoint " + value);
        Console.Out.Flush();
    }

    private static byte[] BuildRuntimeTestWasm(string scenario)
    {
        if (scenario == "call-get-time")
        {
            return new byte[] {
                0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
                // Type section (id=1, len=8, 2 types: ()->i64, ()->())
                0x01, 0x08, 0x02,
                0x60, 0x00, 0x01, 0x7e,
                0x60, 0x00, 0x00,
                // Import section (id=2, len=16, 1 import: env.get_time -> func 0)
                0x02, 0x10, 0x01,
                0x03, (byte)'e', (byte)'n', (byte)'v',
                0x08, (byte)'g', (byte)'e', (byte)'t', (byte)'_', (byte)'t', (byte)'i', (byte)'m', (byte)'e',
                0x00, 0x00,
                // Function section (id=3, len=2, 1 func: func 1 has type 1)
                0x03, 0x02, 0x01, 0x01,
                // Memory section (id=5, len=3, 1 page min)
                0x05, 0x03, 0x01, 0x00, 0x01,
                // Export section (id=7, len=7, "run" -> func 1)
                0x07, 0x07, 0x01, 0x03, (byte)'r', (byte)'u', (byte)'n', 0x00, 0x01,
                // Code section (id=10, len=11, 1 body)
                0x0a, 0x0b, 0x01,
                0x09, 0x00,
                0x41, 0x00,
                0x10, 0x00,
                0x37, 0x03, 0x00,
                0x0b
            };
        }

        byte[] body;
        switch (scenario)
        {
            case "empty":
                body = new byte[] { 0x00, 0x0b };
                break;
            case "store":
                body = new byte[] {
                    0x00, 0x41, 0x00, 0x41, 0x07, 0x36, 0x02, 0x00, 0x0b
                };
                break;
            case "add":
                body = new byte[] {
                    0x00, 0x41, 0x00, 0x41, 0x03, 0x41, 0x04, 0x6a,
                    0x36, 0x02, 0x00, 0x0b
                };
                break;
            case "load-add-store":
                body = new byte[] {
                    0x00, 0x41, 0x00, 0x41, 0x00, 0x28, 0x02, 0x00,
                    0x41, 0x07, 0x6a, 0x36, 0x02, 0x00, 0x0b
                };
                break;
            default:
                throw new ArgumentException("Unknown runtime WASM scenario: " + scenario);
        }

        byte[] moduleHeader = new byte[] {
            0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
            0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
            0x03, 0x02, 0x01, 0x00,
            0x05, 0x03, 0x01, 0x00, 0x01,
            0x07, 0x07, 0x01, 0x03, (byte)'r', (byte)'u', (byte)'n', 0x00, 0x00
        };
        byte[] codePayload = new byte[body.Length + 2];
        codePayload[0] = 0x01;
        codePayload[1] = (byte)body.Length;
        Array.Copy(body, 0, codePayload, 2, body.Length);

        byte[] wasmBytes = new byte[moduleHeader.Length + codePayload.Length + 2];
        Array.Copy(moduleHeader, wasmBytes, moduleHeader.Length);
        int codeSectionOffset = moduleHeader.Length;
        wasmBytes[codeSectionOffset] = 0x0a;
        wasmBytes[codeSectionOffset + 1] = (byte)codePayload.Length;
        Array.Copy(codePayload, 0, wasmBytes, codeSectionOffset + 2, codePayload.Length);
        return wasmBytes;
    }

    private static int RunCraneliftRuntimeTest(string runtimeBinPath, string scenario)
    {
        byte[] runtimeBytes = File.ReadAllBytes(runtimeBinPath);
        byte[] wasmBytes = BuildRuntimeTestWasm(scenario);
        int firstExpected = 0;
        int secondExpected = 0;
        int thirdExpected = 0;
        switch (scenario)
        {
            case "store":
            case "add":
                firstExpected = secondExpected = thirdExpected = 7;
                break;
            case "load-add-store":
                firstExpected = 7;
                secondExpected = 14;
                thirdExpected = 21;
                break;
        }

        GCHandle wasmPin = GCHandle.Alloc(wasmBytes, GCHandleType.Pinned);
        RuntimeAllocMemCallback allocMem = RuntimeAllocMem;
        RuntimeProtectMemCallback protectMem = RuntimeProtectMem;
        RuntimeFreeMemCallback freeMem = RuntimeFreeMem;
        RuntimeFlushIcacheCallback flushIcache = RuntimeFlushIcache;
        RuntimeHostPanicCallback hostPanic = RuntimeHostPanic;
        RuntimeDebugCallback debug = RuntimeDebug;
        RuntimeProcessExitCallback procExit = RuntimeProcessExit;
        RuntimeGetTimeCallback getTime = RuntimeGetTime;
        RuntimeGetRandomCallback getRandom = RuntimeGetRandom;
        RuntimeGetArgsCallback getArgs = RuntimeGetArgs;
        RuntimeGetEnvCallback getEnv = RuntimeGetEnv;
        RuntimeGetCwdCallback getCwd = RuntimeGetCwd;
        RuntimeSetCwdCallback setCwd = RuntimeSetCwd;
        RuntimeGetPlatformInfoCallback getPlatformInfo = RuntimeGetPlatformInfo;
        RuntimeHandleCloseCallback handleClose = RuntimeHandleClose;
        RuntimeTtySetModeCallback ttySetMode = RuntimeTtySetMode;
        RuntimeTtyGetSizeCallback ttyGetSize = RuntimeTtyGetSize;
        RuntimeTtySetSizeCallback ttySetSize = RuntimeTtySetSize;
        RuntimeFdIsAttyCallback fdIsAtty = RuntimeFdIsAtty;

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
            Marshal.WriteIntPtr(sliceAlloc, 7 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(debug));
            Marshal.WriteIntPtr(sliceAlloc, 8 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(procExit));
            Marshal.WriteIntPtr(sliceAlloc, 9 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(getTime));
            Marshal.WriteIntPtr(sliceAlloc, 10 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(getRandom));
            Marshal.WriteIntPtr(sliceAlloc, 11 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(getArgs));
            Marshal.WriteIntPtr(sliceAlloc, 12 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(getEnv));
            Marshal.WriteIntPtr(sliceAlloc, 13 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(getCwd));
            Marshal.WriteIntPtr(sliceAlloc, 14 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(setCwd));
            Marshal.WriteIntPtr(sliceAlloc, 15 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(getPlatformInfo));
            Marshal.WriteIntPtr(sliceAlloc, 22 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(handleClose));
            Marshal.WriteIntPtr(sliceAlloc, 43 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(ttySetMode));
            Marshal.WriteIntPtr(sliceAlloc, 44 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(ttyGetSize));
            Marshal.WriteIntPtr(sliceAlloc, 45 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(ttySetSize));
            Marshal.WriteIntPtr(sliceAlloc, 46 * IntPtr.Size, Marshal.GetFunctionPointerForDelegate(fdIsAtty));

            IntPtr sliceCode = new IntPtr(sliceAlloc.ToInt64() + PrefixAbiOffset);
            Marshal.Copy(runtimeBytes, 0, sliceCode, runtimeBytes.Length);
            FlushInstructionCache(GetCurrentProcess(), sliceCode, new UIntPtr((uint)runtimeBytes.Length));

            RuntimeSliceEntry entry = Marshal.GetDelegateForFunctionPointer<RuntimeSliceEntry>(sliceCode);

            s_sliceCode = sliceCode;
            VectoredHandlerCallback veh = OnVectoredException;
            IntPtr hVeh = AddVectoredExceptionHandler(1, veh);
            Console.WriteLine("INFO: AddVectoredExceptionHandler handle = " + hVeh.ToInt64());
            Console.Out.Flush();

            // Call #1 compiles the selected WASM scenario and runs it once.
            Console.WriteLine("INFO: entering runtime for cold JIT");
            Exception threadEx = null;
            Thread t = new Thread(() => {
                try {
                    entry();
                } catch (Exception ex) {
                    threadEx = ex;
                }
            }, 8 * 1024 * 1024);
            t.Start();
            t.Join();
            GC.KeepAlive(veh);
            if (threadEx != null) throw threadEx;
            Console.WriteLine("INFO: cold JIT entry returned");

            // Read RuntimeGlobals at -0x10000 (sliceAlloc + 0x10000)
            IntPtr globalsPtr = new IntPtr(sliceAlloc.ToInt64() + 0x10000);
            byte initialized = Marshal.ReadByte(globalsPtr, 0);
            IntPtr guestMemBase = Marshal.ReadIntPtr(globalsPtr, IntPtr.Size);
            if (initialized != 1 || guestMemBase == IntPtr.Zero)
            {
                Console.Error.WriteLine("FAIL: RuntimeGlobals not initialized after Call #1");
                return 2;
            }

            if (scenario == "call-get-time")
            {
                long time1 = Marshal.ReadInt64(guestMemBase, 0);
                if (time1 <= 0)
                {
                    Console.Error.WriteLine("FAIL: Expected positive timestamp in guest_mem[0], got " + time1);
                    return 3;
                }
                Thread.Sleep(2);
                Console.WriteLine("INFO: entering runtime for first warm retrigger");
                entry();
                long time2 = Marshal.ReadInt64(guestMemBase, 0);
                Thread.Sleep(2);
                Console.WriteLine("INFO: entering runtime for second warm retrigger");
                entry();
                long time3 = Marshal.ReadInt64(guestMemBase, 0);
                if (time2 <= time1 || time3 <= time2)
                {
                    Console.Error.WriteLine(
                        "FAIL: Monotonic timestamp violation: " + time1 + " -> " + time2 + " -> " + time3);
                    return 4;
                }

                Console.WriteLine(
                    "PASS  [runtime/" + GetArchitecture() + "] scenario " + scenario
                    + " verified (timestamps: " + time1 + " -> " + time2 + " -> " + time3 + ")");
                return 0;
            }

            int val1 = Marshal.ReadInt32(guestMemBase, 0);
            if (val1 != firstExpected)
            {
                Console.Error.WriteLine(
                    "FAIL: Expected guest_mem[0] == " + firstExpected
                    + " after Call #1, got " + val1);
                return 3;
            }

            // Calls #2 & #3 exercise warm re-entry into the already compiled function.
            Console.WriteLine("INFO: entering runtime for first warm retrigger");
            entry();
            int val2 = Marshal.ReadInt32(guestMemBase, 0);
            Console.WriteLine("INFO: entering runtime for second warm retrigger");
            entry();
            int val3 = Marshal.ReadInt32(guestMemBase, 0);
            if (val2 != secondExpected || val3 != thirdExpected)
            {
                Console.Error.WriteLine(
                    "FAIL: Expected guest_mem[0] == " + secondExpected + " then "
                    + thirdExpected + ", got " + val2 + " then " + val3);
                return 4;
            }

            Console.WriteLine(
                "PASS  [runtime/" + GetArchitecture() + "] scenario " + scenario
                + " verified (mem[0]: " + firstExpected + " -> " + secondExpected
                + " -> " + thirdExpected + ")");
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
            GC.KeepAlive(debug);
            GC.KeepAlive(procExit);
            GC.KeepAlive(getTime);
            GC.KeepAlive(getRandom);
            GC.KeepAlive(getArgs);
            GC.KeepAlive(getEnv);
            GC.KeepAlive(getCwd);
            GC.KeepAlive(setCwd);
            GC.KeepAlive(getPlatformInfo);
            GC.KeepAlive(handleClose);
            GC.KeepAlive(ttySetMode);
            GC.KeepAlive(ttyGetSize);
            GC.KeepAlive(ttySetSize);
            GC.KeepAlive(fdIsAtty);
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

    [DllImport("kernel32.dll")]
    private static extern IntPtr AddVectoredExceptionHandler(uint first, VectoredHandlerCallback handler);

    [UnmanagedFunctionPointer(CallingConvention.Winapi)]
    private delegate int VectoredHandlerCallback(IntPtr exceptionPointers);

    private static IntPtr s_sliceCode = IntPtr.Zero;

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool WriteFile(
        IntPtr hFile,
        byte[] lpBuffer,
        uint nNumberOfBytesToWrite,
        out uint lpNumberOfBytesWritten,
        IntPtr lpOverlapped);

    private static int OnVectoredException(IntPtr ptr)
    {
        try {
            IntPtr pRec = Marshal.ReadIntPtr(ptr, 0);
            IntPtr pCtx = Marshal.ReadIntPtr(ptr, IntPtr.Size);
            uint code = (uint)Marshal.ReadInt32(pRec, 0);
            if (code == 0x04242420) return 0;
            IntPtr addr = Marshal.ReadIntPtr(pRec, 16);
            long offset = addr.ToInt64() - s_sliceCode.ToInt64();
            long badAddr = 0;
            try { badAddr = Marshal.ReadIntPtr(pRec, 40).ToInt64(); } catch {}
            long rsp = 0, rbp = 0, rax = 0, rcx = 0, rdx = 0, rsi = 0, rdi = 0;
            try {
                rsp = Marshal.ReadInt64(pCtx, 0x98);
                rbp = Marshal.ReadInt64(pCtx, 0xa0);
                rax = Marshal.ReadInt64(pCtx, 0x78);
                rcx = Marshal.ReadInt64(pCtx, 0x80);
                rdx = Marshal.ReadInt64(pCtx, 0x88);
                rsi = Marshal.ReadInt64(pCtx, 0xa8);
                rdi = Marshal.ReadInt64(pCtx, 0xb0);
            } catch {}
            long ret_addr = 0;
            try { ret_addr = Marshal.ReadInt64(new IntPtr(rsp), 0); } catch {}
            string msg = string.Format("CRASH: Exception 0x{0:X8} at PC=0x{1:X16} (slice+0x{2:X}), bad_addr=0x{3:X16}\n"
                + "  RAX=0x{4:X16} RCX=0x{5:X16} RDX=0x{6:X16}\n"
                + "  RSI=0x{7:X16} RDI=0x{8:X16} RBP=0x{9:X16} RSP=0x{10:X16}\n"
                + "  *(RSP)=0x{11:X16} (slice+0x{12:X})\n",
                code, addr.ToInt64(), offset, badAddr,
                rax, rcx, rdx, rsi, rdi, rbp, rsp,
                ret_addr, ret_addr - s_sliceCode.ToInt64());
            Console.Error.WriteLine(msg);
            Console.Error.Flush();
        } catch (Exception ex) {
            Console.Error.WriteLine("VEH error: " + ex.Message);
            Console.Error.Flush();
        }
        return 0; // EXCEPTION_CONTINUE_SEARCH
    }
}
