using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;
using System.Text.RegularExpressions;
using ZstdSharp;

internal static class PackComponents
{
    private const int MaximumComponentSize = 8192;
    private const int MaximumCompressedComponentSize = 8192;
    private const int MaximumBrotSize = 16 * 1024 * 1024;
    private const int DosHeaderSize = 60;

    private struct ComponentRange
    {
        public long Offset;
        public long Length;
    }

    private static int Main(string[] args)
    {
        if (args.Length != 0 && args[0] == "--assemble")
        {
            return Assemble(args);
        }
        if (args.Length == 0 || args.Length % 2 != 0)
        {
            Console.Error.WriteLine(
                "Usage: PackComponents input output [input output ...]\n" +
                "   or: PackComponents --assemble windows.exe bootstrap.in " +
                "output.exe unix-assets-dir");
            return 2;
        }

        using (var compressor = new Compressor(3))
        using (var decompressor = new Decompressor())
        {
            for (int i = 0; i < args.Length; i += 2)
            {
                byte[] source = File.ReadAllBytes(args[i]);
                byte[] compressed = compressor.Wrap(source).ToArray();
                byte[] restored = decompressor.Unwrap(compressed).ToArray();
                if (!AreEqual(source, restored))
                {
                    Console.Error.WriteLine(
                        "Zstandard round-trip check failed: " + args[i]);
                    return 1;
                }
                File.WriteAllBytes(args[i + 1], compressed);
            }
        }
        return 0;
    }

    private static int Assemble(string[] args)
    {
        if (args.Length != 5)
        {
            Console.Error.WriteLine(
                "Usage: PackComponents --assemble windows.exe bootstrap.in " +
                "output.exe unix-assets-dir");
            return 2;
        }

        string windowsExecutablePath = args[1];
        string bootstrapPath = args[2];
        string outputPath = args[3];
        string assetsDirectory = args[4];
        byte[] windowsExecutable = File.ReadAllBytes(windowsExecutablePath);
        ValidatePortableExecutable(windowsExecutable);

        string bootstrap = File.ReadAllText(bootstrapPath)
            .Replace("\r\n", "\n")
            .Replace('\r', '\n');
        if (bootstrap.StartsWith("#!", StringComparison.Ordinal))
        {
            throw new InvalidOperationException(
                "The Unix bootstrap must not have a shebang.");
        }

        using (var bundle = new MemoryStream())
        {
            bundle.Write(windowsExecutable, 0, windowsExecutable.Length);
            var cachedAssets = new Dictionary<string, ComponentRange>(
                StringComparer.OrdinalIgnoreCase);
            var replacements = new Dictionary<string, string>();

            AddAsset(bundle, cachedAssets, replacements, assetsDirectory,
                "brot-x86_64-unknown-linux-gnu", false, "BROT_LINUX_X86_64");
            AddAsset(bundle, cachedAssets, replacements, assetsDirectory,
                "brot-aarch64-unknown-linux-gnu", false, "BROT_LINUX_AARCH64");
            AddAsset(bundle, cachedAssets, replacements, assetsDirectory,
                "brot-aarch64-linux-android", false, "BROT_ANDROID_AARCH64");
            AddAsset(bundle, cachedAssets, replacements, assetsDirectory,
                "brot-x86_64-apple-darwin", false, "BROT_DARWIN_X86_64");
            AddAsset(bundle, cachedAssets, replacements, assetsDirectory,
                "brot-aarch64-apple-darwin", false, "BROT_DARWIN_AARCH64");

            AddAsset(bundle, cachedAssets, replacements, assetsDirectory,
                "platform-linux-x86_64.bin", true, "PLATFORM_LINUX_X86_64");
            AddAsset(bundle, cachedAssets, replacements, assetsDirectory,
                "platform-linux-aarch64.bin", true, "PLATFORM_LINUX_AARCH64");
            AddAsset(bundle, cachedAssets, replacements, assetsDirectory,
                "platform-linux-aarch64.bin", true, "PLATFORM_ANDROID_AARCH64");
            AddAsset(bundle, cachedAssets, replacements, assetsDirectory,
                "platform-darwin-x86_64.bin", true, "PLATFORM_DARWIN_X86_64");
            AddAsset(bundle, cachedAssets, replacements, assetsDirectory,
                "platform-darwin-aarch64.bin", true, "PLATFORM_DARWIN_AARCH64");

            AddAsset(bundle, cachedAssets, replacements, assetsDirectory,
                "hello-x86_64.bin", true, "HELLO_X86_64");
            AddAsset(bundle, cachedAssets, replacements, assetsDirectory,
                "hello-aarch64.bin", true, "HELLO_AARCH64");

            long bootstrapOffset = bundle.Position;
            foreach (KeyValuePair<string, string> replacement in replacements)
            {
                bootstrap = bootstrap.Replace(
                    "@" + replacement.Key + "@",
                    replacement.Value);
            }
            if (Regex.IsMatch(bootstrap, "@[A-Z0-9_]+@"))
            {
                throw new InvalidOperationException(
                    "The Unix bootstrap contains unresolved component offsets.");
            }

            byte[] bootstrapBytes = new UTF8Encoding(false).GetBytes(bootstrap);
            bundle.Write(bootstrapBytes, 0, bootstrapBytes.Length);
            byte[] completeBundle = bundle.ToArray();
            PatchDosStub(completeBundle, bootstrapOffset + 1);
            File.WriteAllBytes(outputPath, completeBundle);
        }

        Console.WriteLine("Assembled the single Windows/Unix bundle: " + outputPath);
        return 0;
    }

    private static void AddAsset(
        MemoryStream bundle,
        IDictionary<string, ComponentRange> cachedAssets,
        IDictionary<string, string> replacements,
        string assetsDirectory,
        string fileName,
        bool compress,
        string token)
    {
        string path = Path.GetFullPath(Path.Combine(assetsDirectory, fileName));
        string cacheKey = path + (compress ? "|zstd" : "|raw");
        ComponentRange range;
        if (!cachedAssets.TryGetValue(cacheKey, out range))
        {
            byte[] data = File.ReadAllBytes(path);
            if (data.Length == 0)
            {
                throw new InvalidOperationException(
                    "Component is empty: " + path);
            }

            if (compress)
            {
                if (data.Length > MaximumComponentSize)
                {
                    throw new InvalidOperationException(
                        "Flat component exceeds 8 KiB: " + path);
                }
                data = CompressAndVerify(data);
                if (data.Length > MaximumCompressedComponentSize)
                {
                    throw new InvalidOperationException(
                        "Compressed component exceeds 8 KiB: " + path);
                }
            }
            else if (data.Length > MaximumBrotSize)
            {
                throw new InvalidOperationException(
                    "Brot executable exceeds 16 MiB: " + path);
            }

            range = new ComponentRange
            {
                Offset = bundle.Position,
                Length = data.LongLength,
            };
            bundle.Write(data, 0, data.Length);
            cachedAssets.Add(cacheKey, range);
        }

        replacements[token + "_OFFSET"] =
            range.Offset.ToString(CultureInfo.InvariantCulture);
        replacements[token + "_LENGTH"] =
            range.Length.ToString(CultureInfo.InvariantCulture);
    }

    private static byte[] CompressAndVerify(byte[] source)
    {
        using (var compressor = new Compressor(3))
        using (var decompressor = new Decompressor())
        {
            byte[] compressed = compressor.Wrap(source).ToArray();
            byte[] restored = decompressor.Unwrap(compressed).ToArray();
            if (!AreEqual(source, restored))
            {
                throw new InvalidOperationException(
                    "Zstandard round-trip check failed during assembly.");
            }
            return compressed;
        }
    }

    private static void ValidatePortableExecutable(byte[] executable)
    {
        if (executable.Length < 64 ||
            executable[0] != (byte)'M' ||
            executable[1] != (byte)'Z')
        {
            throw new InvalidOperationException(
                "Windows host is not an MZ executable.");
        }

        int peOffset = BitConverter.ToInt32(executable, 0x3c);
        if (peOffset < 64 ||
            peOffset > executable.Length - 4 ||
            executable[peOffset] != (byte)'P' ||
            executable[peOffset + 1] != (byte)'E' ||
            executable[peOffset + 2] != 0 ||
            executable[peOffset + 3] != 0)
        {
            throw new InvalidOperationException(
                "Windows host has an invalid PE header.");
        }
    }

    private static void PatchDosStub(byte[] executable, long bootstrapOffset)
    {
        string command = string.Format(
            CultureInfo.InvariantCulture,
            "MZ=1;bash <(tail -c +{0} \"$0\") \"$0\" \"$@\";exit\n",
            bootstrapOffset);
        byte[] commandBytes = Encoding.ASCII.GetBytes(command);
        if (commandBytes.Length > DosHeaderSize)
        {
            throw new InvalidOperationException(
                "The MZ/Bash trampoline does not fit in the DOS stub.");
        }

        Array.Copy(commandBytes, executable, commandBytes.Length);
    }

    private static bool AreEqual(byte[] left, byte[] right)
    {
        if (left.Length != right.Length)
        {
            return false;
        }
        for (int i = 0; i < left.Length; i++)
        {
            if (left[i] != right[i])
            {
                return false;
            }
        }
        return true;
    }
}
