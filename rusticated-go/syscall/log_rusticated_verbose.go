//go:build verbose

package syscall

func debugPrintArg(arg interface{}) {
	switch value := arg.(type) {
	case string:
		print(value)
	case int:
		print(value)
	case int32:
		print(value)
	case int64:
		print(value)
	case uint32:
		print(value)
	case uint64:
		print(value)
	case uintptr:
		print(value)
	case bool:
		print(value)
	default:
		print("<unsupported>")
	}
}

func debugPrintln(args ...interface{}) {
	for i, arg := range args {
		if i > 0 {
			print(" ")
		}
		debugPrintArg(arg)
	}
	println()
}
