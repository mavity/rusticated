//go:build wasip1

package syscall

import "unsafe"

//go:wasmimport env process_exit
func ProcExit(code int32)

//go:wasmimport env process_spawn
//go:noescape
func rusticated_process_spawn(overlapped unsafe.Pointer, cfgPtr *byte, cfgLen uint32)

//go:wasmimport env process_wait
//go:noescape
func rusticated_process_wait(overlapped unsafe.Pointer, handle uint64)
