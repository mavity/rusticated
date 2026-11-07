using System;
using System.IO;
using ZstdSharp;

internal static class PackComponents
{
    private static int Main(string[] args)
    {
        if (args.Length == 0 || args.Length % 2 != 0)
        {
            Console.Error.WriteLine(
                "Usage: PackComponents input output [input output ...]");
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
