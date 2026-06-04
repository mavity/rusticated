//go:build wasip1

package syscall

//go:wasmimport env process_exit
func ProcExit(code int32)
