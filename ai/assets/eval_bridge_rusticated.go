//go:build wasip1

package assets

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

const (
	i64 = 0x7E
)

var (
	evalOnce    sync.Once
	evalLib     uint64
	evalSym     uint64
	evalInitErr error
)

func initEvalBridge() error {
	evalOnce.Do(func() {
		lib, err := syscall.DylibOpen("eval", syscall.RTLD_NOW)
		if err != nil {
			evalInitErr = fmt.Errorf("open virtual eval dylib: %w", err)
			return
		}
		sym, err := syscall.DylibSym(lib, "eval")
		if err != nil {
			evalInitErr = fmt.Errorf("resolve virtual eval symbol: %w", err)
			return
		}
		evalLib = lib
		evalSym = sym
	})
	return evalInitErr
}

// EvalJS evaluates jsCode in the browser host's global scope, parking the calling
// Go goroutine if jsCode returns a Promise, and returns the resolved string.
func EvalJS(jsCode string) (string, error) {
	if err := initEvalBridge(); err != nil {
		return "", err
	}
	if jsCode == "" {
		return "", nil
	}

	codeBytes := []byte(jsCode)
	outBuf := make([]byte, 16*1024)
	var actualLen uint32

	sig := []byte{0x60, 5, i64, i64, i64, i64, i64, 1, i64}
	args := []uint64{
		uint64(uintptr(unsafe.Pointer(&codeBytes[0]))),
		uint64(len(codeBytes)),
		uint64(uintptr(unsafe.Pointer(&outBuf[0]))),
		uint64(len(outBuf)),
		uint64(uintptr(unsafe.Pointer(&actualLen))),
	}

	res, err := syscall.DylibCall(evalSym, sig, args, 0)
	runtime.KeepAlive(codeBytes)
	runtime.KeepAlive(outBuf)
	runtime.KeepAlive(&actualLen)
	if err != nil {
		return "", err
	}

	rc := res[0]
	if rc == 2 && actualLen > uint32(len(outBuf)) {
		// Output exceeded initial 16KB buffer; fetch the stashed overflow from JS
		bigBuf := make([]byte, actualLen)
		fetchCode := []byte("globalThis.__rustica_take_overflow && globalThis.__rustica_take_overflow()")
		var bigActual uint32
		args2 := []uint64{
			uint64(uintptr(unsafe.Pointer(&fetchCode[0]))),
			uint64(len(fetchCode)),
			uint64(uintptr(unsafe.Pointer(&bigBuf[0]))),
			uint64(len(bigBuf)),
			uint64(uintptr(unsafe.Pointer(&bigActual))),
		}
		res2, err2 := syscall.DylibCall(evalSym, sig, args2, 0)
		runtime.KeepAlive(fetchCode)
		runtime.KeepAlive(bigBuf)
		runtime.KeepAlive(&bigActual)
		if err2 != nil {
			return "", err2
		}
		if res2[0] != 0 {
			return "", errors.New(string(bigBuf[:min(int(bigActual), len(bigBuf))]))
		}
		return string(bigBuf[:min(int(bigActual), len(bigBuf))]), nil
	}

	n := min(int(actualLen), len(outBuf))
	outStr := string(outBuf[:n])
	if rc != 0 {
		return "", errors.New(outStr)
	}
	return outStr, nil
}
