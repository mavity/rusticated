//go:build verbose

package syscall

func debugPrintln(args ...interface{}) {
	for i, arg := range args {
		if i > 0 {
			print(" ")
		}
		print(arg)
	}
	println()
}
