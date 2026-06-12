//go:build wasip1

package runtime

import _ "unsafe"

//go:nosplit
func pause(newsp uintptr) {
}

//go:wasmimport env rusticated_debug
func rusticated_debug(val int32)
