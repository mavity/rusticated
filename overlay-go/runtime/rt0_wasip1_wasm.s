// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "textflag.h"

TEXT _rt0_wasm_wasip1(SB),NOSPLIT,$0
	Unreachable
	Return

TEXT _rt0_wasm_wasip1_lib(SB),NOSPLIT,$0
	I32Const $runtime-initialized(SB)
	I32Load $0
	I32Eqz
	If
		I32Const $runtime-initialized(SB)
		I32Const $1
		I32Store $0

		MOVD $runtime·wasmStack+(m0Stack__size-16)(SB), SP

		I32Const $0 // entry PC_B
		Call runtime·rt0_go(SB)
		Drop
		Call wasm_pc_f_loop(SB)
	Else
		Call runtime-handleContinuation(SB)
		I32Const $0
		Set PAUSE
		Call wasm_pc_f_loop(SB)
	End

	Return
