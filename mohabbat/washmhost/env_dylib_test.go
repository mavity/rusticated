package main

import (
	"context"
	"encoding/binary"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tetratelabs/wazero/api"
)

// Mock wazero api.Module for testing
type mockDylibModule struct {
	api.Module
	mem *mockDylibMemory
	fns map[string]api.Function
}

func (m *mockDylibModule) Memory() api.Memory {
	return m.mem
}

func (m *mockDylibModule) ExportedFunction(name string) api.Function {
	return m.fns[name]
}

// Mock wazero api.Memory for testing
type mockDylibMemory struct {
	api.Memory
	buf []byte
}

func (m *mockDylibMemory) Size() uint32 {
	return uint32(len(m.buf))
}

func (m *mockDylibMemory) Read(offset uint32, byteCount uint32) ([]byte, bool) {
	if offset+byteCount > uint32(len(m.buf)) {
		return nil, false
	}
	return m.buf[offset : offset+byteCount], true
}

func (m *mockDylibMemory) Write(offset uint32, b []byte) bool {
	if offset+uint32(len(b)) > uint32(len(m.buf)) {
		return false
	}
	copy(m.buf[offset:], b)
	return true
}

// Mock wazero api.Function for testing
type mockFunction struct {
	api.Function
	callFunc func(args []uint64) ([]uint64, error)
}

func (f *mockFunction) Call(ctx context.Context, args ...uint64) ([]uint64, error) {
	return f.callFunc(args)
}

func getTestLibPath() string {
	switch runtime.GOOS {
	case "darwin":
		return "/usr/lib/libSystem.B.dylib"
	case "windows":
		return "msvcrt.dll"
	default:
		// Linux
		paths := []string{"/lib/x86_64-linux-gnu/libm.so.6", "/lib64/libm.so.6", "/lib/libm.so.6", "libm.so.6"}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		return "libm.so.6"
	}
}

func waitCompleted(t *testing.T, env *HostEnv, mem *mockDylibMemory, ovPtr uint32) {
	deadline := time.Now().Add(1 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("Timeout waiting for overlapped completion")
		}
		select {
		case op := <-env.fileOpsQueue:
			op()
			if binary.LittleEndian.Uint32(mem.buf[ovPtr:ovPtr+4]) == 1 {
				return
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestDylibLoadAndCall(t *testing.T) {
	env := NewHostEnv()
	defer env.Close()

	libPath := getTestLibPath()

	mem := &mockDylibMemory{buf: make([]byte, 1024)}
	mod := &mockDylibModule{mem: mem}

	// 1. Test dylib_open
	ovPtr := uint32(10)
	pathPtr := uint32(100)
	copy(mem.buf[pathPtr:], libPath)

	stack := []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(libPath)), 0}
	env.sys_dylib_open(context.Background(), mod, stack)

	waitCompleted(t, env, mem, ovPtr)

	// Read completion
	completed := binary.LittleEndian.Uint32(mem.buf[ovPtr : ovPtr+4])
	errCode := binary.LittleEndian.Uint32(mem.buf[ovPtr+4 : ovPtr+8])
	libHandle := binary.LittleEndian.Uint64(mem.buf[ovPtr+16 : ovPtr+24])

	if completed != 1 {
		t.Fatal("Expected overlapped operation to complete")
	}
	if errCode != 0 {
		t.Fatalf("dylib_open failed with error code: %d", errCode)
	}
	if libHandle == 0 {
		t.Fatal("Expected non-zero dylib handle")
	}

	// 2. Test dylib_sym (abs)
	symName := "abs"
	ovPtr2 := uint32(40)
	namePtr := uint32(200)
	copy(mem.buf[namePtr:], symName)

	stack2 := []uint64{uint64(ovPtr2), libHandle, uint64(namePtr), uint64(len(symName))}
	env.sys_dylib_sym(context.Background(), mod, stack2)

	// dylib_sym is synchronous, check immediately
	completed2 := binary.LittleEndian.Uint32(mem.buf[ovPtr2 : ovPtr2+4])
	errCode2 := binary.LittleEndian.Uint32(mem.buf[ovPtr2+4 : ovPtr2+8])
	symHandle := binary.LittleEndian.Uint64(mem.buf[ovPtr2+16 : ovPtr2+24])

	if completed2 != 1 {
		t.Fatal("Expected dylib_sym to complete synchronously")
	}
	if errCode2 != 0 {
		t.Fatalf("dylib_sym failed with error: %d", errCode2)
	}
	if symHandle == 0 {
		t.Fatal("Expected non-zero symbol handle")
	}

	// 3. Test dylib_call (abs(-42))
	ovPtr3 := uint32(70)
	descPtr := uint32(300)

	// Build Call Descriptor
	// cc=0 (cdecl), arg_count=1, ret_type=i32 (0x01), reserved=0
	desc := make([]byte, 100)
	desc[0] = 0
	desc[1] = 1
	desc[2] = 0x01
	desc[3] = 0

	// Arg 1: tag=i32 (0x01), val=-42
	desc[4] = 0x01
	binary.LittleEndian.PutUint32(desc[5:9], 0xFFFFFFD6)

	copy(mem.buf[descPtr:], desc[:9])

	stack3 := []uint64{uint64(ovPtr3), symHandle, uint64(descPtr), 9}
	env.sys_dylib_call(context.Background(), mod, stack3)

	waitCompleted(t, env, mem, ovPtr3)

	// Read completion
	completed3 := binary.LittleEndian.Uint32(mem.buf[ovPtr3 : ovPtr3+4])
	errCode3 := binary.LittleEndian.Uint32(mem.buf[ovPtr3+4 : ovPtr3+8])
	retVal := binary.LittleEndian.Uint64(mem.buf[ovPtr3+16 : ovPtr3+24])

	if completed3 != 1 {
		t.Fatal("Expected dylib_call to complete")
	}
	if errCode3 != 0 {
		t.Fatalf("dylib_call failed with error: %d", errCode3)
	}

	resVal := int32(retVal)
	expectedVal := int32(42)

	if resVal != expectedVal {
		t.Fatalf("Expected abs(-42) to be %d, got %d", expectedVal, resVal)
	}

	// 4. Test dylib_close
	env.sys_dylib_close(context.Background(), mod, []uint64{libHandle})

	// Verify handle was removed
	env.mu.Lock()
	_, exists := env.handles[libHandle]
	env.mu.Unlock()
	if exists {
		t.Fatal("Expected libHandle to be removed after closing")
	}
}

func TestCallbackTrampolines(t *testing.T) {
	env := NewHostEnv()
	defer env.Close()

	mem := &mockDylibMemory{buf: make([]byte, 1024)}
	mod := &mockDylibModule{
		mem: mem,
		fns: make(map[string]api.Function),
	}

	// Set owning GID
	env.setOwningGID()

	// Register mock guest function
	var guestFnCalled int32
	var guestFnArgs []uint64
	mod.fns["test_guest_cb"] = &mockFunction{
		callFunc: func(args []uint64) ([]uint64, error) {
			atomic.AddInt32(&guestFnCalled, 1)
			guestFnArgs = args
			return []uint64{42}, nil
		},
	}

	// 1. Same-thread callback test
	sig := CallbackSig{
		ArgCount: 2,
		RetType:  0x04, // u64
		ArgTypes: []byte{0x04, 0x04},
	}

	cbHandle := uint64(5)
	env.handles[cbHandle] = &CallbackState{
		Parent:     99,
		Sig:        sig,
		GuestFn:    "test_guest_cb",
		Trampoline: 0,
	}

	// Invoke callback inline (simulating same-thread)
	ret := env.invokeCallback(mod, cbHandle, sig, "test_guest_cb", []uintptr{100, 200})

	if atomic.LoadInt32(&guestFnCalled) != 1 {
		t.Fatal("Expected guest function to be called")
	}
	if ret != 42 {
		t.Fatalf("Expected callback return value 42, got %d", ret)
	}
	if len(guestFnArgs) != 2 || guestFnArgs[0] != 100 || guestFnArgs[1] != 200 {
		t.Fatalf("Expected guest fn args [100, 200], got %v", guestFnArgs)
	}

	// 2. Cross-thread callback test
	// Start a background thread to invoke the callback
	var wg sync.WaitGroup
	wg.Add(1)
	var crossRet uintptr
	go func() {
		defer wg.Done()
		// Will route via channel queue as getGID() != env.owningGID
		crossRet = env.invokeCallback(mod, cbHandle, sig, "test_guest_cb", []uintptr{300, 400})
	}()

	// Wait for callback to be queued, then drain it on the main thread
	select {
	case ev := <-env.callbackQueue:
		env.executeCrossThreadCallback(mod, ev)
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for cross-thread callback")
	}

	wg.Wait()

	if crossRet != 42 {
		t.Fatalf("Expected cross-thread callback return 42, got %d", crossRet)
	}
	if len(guestFnArgs) != 2 || guestFnArgs[0] != 300 || guestFnArgs[1] != 400 {
		t.Fatalf("Expected guest fn args [300, 400], got %v", guestFnArgs)
	}
}
