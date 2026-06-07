//go:build wasip1

package runtime

import _ "unsafe"

// rusticated_pause is defined in asm_rusticated.s
func rusticated_pause(newsp uintptr)

//go:linkname wasm_pc_f_loop wasm_pc_f_loop
func wasm_pc_f_loop()

//go:nosplit
func pause(newsp uintptr) {
	rusticated_pause(newsp)
}

//go:linkname rusticated_pause_asm runtime.rusticated_pause_asm
func rusticated_pause_asm()

//go:wasmimport env rusticated_debug
func rusticated_debug(val int32)
