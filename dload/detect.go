package dload

import (
	"encoding/binary"
	"os"
	"runtime"
	"strings"
)

func detectFlavour(path string) (string, error) {
	// Inspect binary header directly without relying on Pread/ReadAt
	if path != "" {
		if f, err := os.Open(path); err == nil {
			var buf [1024]byte
			n, _ := f.Read(buf[:])
			_ = f.Close()
			if n > 64 {
				// 1. Check PE header (MZ)
				if buf[0] == 'M' && buf[1] == 'Z' {
					peOff := int(binary.LittleEndian.Uint32(buf[0x3c:0x40]))
					if peOff >= 0 && peOff+6 <= n && buf[peOff] == 'P' && buf[peOff+1] == 'E' && buf[peOff+2] == 0 && buf[peOff+3] == 0 {
						machine := binary.LittleEndian.Uint16(buf[peOff+4 : peOff+6])
						switch machine {
						case 0x8664: // IMAGE_FILE_MACHINE_AMD64
							return "windows-amd64", nil
						case 0xAA64: // IMAGE_FILE_MACHINE_ARM64
							return "windows-arm64", nil
						default:
							return "windows-amd64", nil
						}
					}
				}

				// 2. Check ELF header (\x7fELF)
				if buf[0] == 0x7F && buf[1] == 'E' && buf[2] == 'L' && buf[3] == 'F' {
					machine := binary.LittleEndian.Uint16(buf[18:20])
					isAndroid := isAndroidEnvironment()
					switch machine {
					case 62: // EM_X86_64
						return "linux-gnu-amd64", nil
					case 183: // EM_AARCH64
						if isAndroid {
							return "linux-android-arm64", nil
						}
						return "linux-gnu-arm64", nil
					case 40: // EM_ARM
						if isAndroid {
							return "linux-android-arm32", nil
						}
						return "linux-gnu-arm32", nil
					}
				}

				// 3. Check Mach-O header
				magic := binary.BigEndian.Uint32(buf[0:4])
				magicLE := binary.LittleEndian.Uint32(buf[0:4])
				if magic == 0xFEEDFACF || magicLE == 0xFEEDFACF {
					return "darwin-arm64", nil
				}
			}
		}
	}

	// Fallback to host environment detection
	switch runtime.GOOS {
	case "windows":
		if runtime.GOARCH == "arm64" {
			return "windows-arm64", nil
		}
		return "windows-amd64", nil
	case "darwin":
		return "darwin-arm64", nil
	case "linux":
		if isAndroidEnvironment() {
			if runtime.GOARCH == "arm" {
				return "linux-android-arm32", nil
			}
			return "linux-android-arm64", nil
		}
		switch runtime.GOARCH {
		case "arm64":
			return "linux-gnu-arm64", nil
		case "arm":
			return "linux-gnu-arm32", nil
		default:
			return "linux-gnu-amd64", nil
		}
	default:
		// When running under wasip1, default based on host indicators
		if isAndroidEnvironment() {
			return "linux-android-arm64", nil
		}
		return "windows-amd64", nil
	}
}

func isAndroidEnvironment() bool {
	if _, err := os.Stat("/system/bin/linker64"); err == nil {
		return true
	}
	if _, err := os.Stat("/system/bin/linker"); err == nil {
		return true
	}
	if p := os.Getenv("PREFIX"); strings.Contains(p, "com.termux") {
		return true
	}
	if d := os.Getenv("ANDROID_ROOT"); d != "" {
		return true
	}
	return false
}
