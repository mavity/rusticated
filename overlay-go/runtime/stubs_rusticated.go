//go:build wasip1

package runtime

import _ "unsafe"

// pause is defined in asm_rusticated.s
func pause(newsp uintptr)

// wasm_pc_f_loop is defined in asm_rusticated.s
//
//go:linkname wasm_pc_f_loop_rusticated wasm_pc_f_loop_rusticated
func wasm_pc_f_loop_rusticated() int32
