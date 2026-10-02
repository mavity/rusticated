//go:build wasip1

package main

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"syscall"
)

var (
	tflLibOnce sync.Once
	tflLibErr  error
	tflLib     uint64

	symTflModelCreateFromFile  uint64
	symTflModelDelete          uint64
	symTflOptionsCreate        uint64
	symTflOptionsDelete        uint64
	symTflOptionsSetNumThreads uint64
	symTflInterpreterCreate    uint64
	symTflInterpreterDelete    uint64
	symTflInterpreterAllocate  uint64
	symTflInterpreterInvoke    uint64
	symTflGetInputTensor       uint64
	symTflGetOutputTensor      uint64
	symTflTensorCopyFrom       uint64
	symTflTensorCopyTo         uint64
	symTflTensorByteSize       uint64
	symTflTensorNumDims        uint64
	symTflTensorDim            uint64
)

func ensureTFLiteLoaded(libPath string) error {
	tflLibOnce.Do(func() {
		var err error
		tflLib, err = syscall.DylibOpen(libPath, syscall.RTLD_NOW|syscall.RTLD_GLOBAL)
		if err != nil {
			tflLibErr = fmt.Errorf("failed to load TFLite library %s: %w", libPath, err)
			return
		}
		symTflModelCreateFromFile, _ = syscall.DylibSym(tflLib, "TfLiteModelCreateFromFile")
		symTflModelDelete, _ = syscall.DylibSym(tflLib, "TfLiteModelDelete")
		symTflOptionsCreate, _ = syscall.DylibSym(tflLib, "TfLiteInterpreterOptionsCreate")
		symTflOptionsDelete, _ = syscall.DylibSym(tflLib, "TfLiteInterpreterOptionsDelete")
		symTflOptionsSetNumThreads, _ = syscall.DylibSym(tflLib, "TfLiteInterpreterOptionsSetNumThreads")
		symTflInterpreterCreate, _ = syscall.DylibSym(tflLib, "TfLiteInterpreterCreate")
		symTflInterpreterDelete, _ = syscall.DylibSym(tflLib, "TfLiteInterpreterDelete")
		symTflInterpreterAllocate, _ = syscall.DylibSym(tflLib, "TfLiteInterpreterAllocateTensors")
		symTflInterpreterInvoke, _ = syscall.DylibSym(tflLib, "TfLiteInterpreterInvoke")
		symTflGetInputTensor, _ = syscall.DylibSym(tflLib, "TfLiteInterpreterGetInputTensor")
		symTflGetOutputTensor, _ = syscall.DylibSym(tflLib, "TfLiteInterpreterGetOutputTensor")
		symTflTensorCopyFrom, _ = syscall.DylibSym(tflLib, "TfLiteTensorCopyFromBuffer")
		symTflTensorCopyTo, _ = syscall.DylibSym(tflLib, "TfLiteTensorCopyToBuffer")
		symTflTensorByteSize, _ = syscall.DylibSym(tflLib, "TfLiteTensorByteSize")
		symTflTensorNumDims, _ = syscall.DylibSym(tflLib, "TfLiteTensorNumDims")
		symTflTensorDim, _ = syscall.DylibSym(tflLib, "TfLiteTensorDim")
	})
	return tflLibErr
}

// TFLiteInterpreter wraps a TFLite interpreter session for one model.
type TFLiteInterpreter struct {
	ptr uintptr
}

// tflGoStringToCPtr uses the tflLib allocator rather than the LM lib allocator.
func tflGoStringToCPtr(s string) (uint64, func()) {
	ptr, free, err := syscall.CString(tflLib, s)
	if err != nil {
		return 0, func() {}
	}
	return ptr, free
}

func NewTFLiteInterpreter(libPath, modelPath string) (*TFLiteInterpreter, error) {
	if err := ensureTFLiteLoaded(libPath); err != nil {
		return nil, err
	}

	cPath, freePath := tflGoStringToCPtr(modelPath)
	defer freePath()

	modelPtr, err := callDylib(symTflModelCreateFromFile, []byte{i64}, []byte{i64}, cPath)
	if err != nil || modelPtr == 0 {
		return nil, fmt.Errorf("TfLiteModelCreateFromFile failed for %s", modelPath)
	}

	opts, _ := callDylib(symTflOptionsCreate, nil, []byte{i64})
	if opts != 0 && symTflOptionsSetNumThreads != 0 {
		_, _ = callDylib(symTflOptionsSetNumThreads, []byte{i64, i32}, nil, opts, uint64(runtime.NumCPU()))
	}

	interp, err := callDylib(symTflInterpreterCreate, []byte{i64, i64}, []byte{i64}, modelPtr, opts)
	_, _ = callDylib(symTflModelDelete, []byte{i64}, nil, modelPtr)
	if opts != 0 {
		_, _ = callDylib(symTflOptionsDelete, []byte{i64}, nil, opts)
	}
	if err != nil || interp == 0 {
		return nil, fmt.Errorf("TfLiteInterpreterCreate failed for %s", modelPath)
	}

	rc, err := callDylib(symTflInterpreterAllocate, []byte{i64}, []byte{i32}, interp)
	if err != nil || int32(rc) != 0 {
		_, _ = callDylib(symTflInterpreterDelete, []byte{i64}, nil, interp)
		return nil, fmt.Errorf("TfLiteInterpreterAllocateTensors failed")
	}
	return &TFLiteInterpreter{ptr: uintptr(interp)}, nil
}

func (t *TFLiteInterpreter) Close() {
	if t.ptr != 0 {
		_, _ = callDylib(symTflInterpreterDelete, []byte{i64}, nil, uint64(t.ptr))
		t.ptr = 0
	}
}

func (t *TFLiteInterpreter) SetInput(index int, data []float32) error {
	tensor, err := callDylib(symTflGetInputTensor, []byte{i64, i32}, []byte{i64}, uint64(t.ptr), uint64(index))
	if err != nil || tensor == 0 {
		return fmt.Errorf("input tensor %d is nil", index)
	}

	tensorByteSize, _ := callDylib(symTflTensorByteSize, []byte{i64}, []byte{i64}, tensor)

	ndims, _ := callDylib(symTflTensorNumDims, []byte{i64}, []byte{i32}, tensor)
	totalElements := 1
	for i := int32(0); i < int32(ndims); i++ {
		d, _ := callDylib(symTflTensorDim, []byte{i64, i32}, []byte{i32}, tensor, uint64(i))
		totalElements *= int(d)
	}

	byteSize := uint64(len(data) * 4)
	if uint64(tensorByteSize) != byteSize {
		return fmt.Errorf("tensor %d byte size mismatch: model expects %d, got %d", index, tensorByteSize, byteSize)
	}

	buf, err := syscall.DylibAlloc(tflLib, byteSize)
	if err != nil {
		return fmt.Errorf("DylibAlloc failed: %w", err)
	}
	defer func() { _ = syscall.DylibFree(tflLib, buf) }()

	raw := make([]byte, byteSize)
	for i, f := range data {
		b := math.Float32bits(f)
		raw[i*4+0] = byte(b)
		raw[i*4+1] = byte(b >> 8)
		raw[i*4+2] = byte(b >> 16)
		raw[i*4+3] = byte(b >> 24)
	}

	if err := syscall.DylibWriteMem(tflLib, buf, raw); err != nil {
		return fmt.Errorf("DylibWriteMem failed: %w", err)
	}

	rc, _ := callDylib(symTflTensorCopyFrom, []byte{i64, i64, i64}, []byte{i32}, tensor, buf, byteSize)
	if int32(rc) != 0 {
		return fmt.Errorf("TfLiteTensorCopyFromBuffer failed: %d", int32(rc))
	}
	return nil
}

func (t *TFLiteInterpreter) Invoke() error {
	rc, err := callDylib(symTflInterpreterInvoke, []byte{i64}, []byte{i32}, uint64(t.ptr))
	if err != nil || int32(rc) != 0 {
		return fmt.Errorf("TfLiteInterpreterInvoke failed")
	}
	return nil
}

func (t *TFLiteInterpreter) GetOutput(index int) ([]float32, []int32, error) {
	tensor, err := callDylib(symTflGetOutputTensor, []byte{i64, i32}, []byte{i64}, uint64(t.ptr), uint64(index))
	if err != nil || tensor == 0 {
		return nil, nil, fmt.Errorf("output tensor %d is nil", index)
	}
	byteSize, _ := callDylib(symTflTensorByteSize, []byte{i64}, []byte{i64}, tensor)

	buf, err := syscall.DylibAlloc(tflLib, byteSize)
	if err != nil {
		return nil, nil, fmt.Errorf("DylibAlloc failed: %w", err)
	}
	defer func() { _ = syscall.DylibFree(tflLib, buf) }()

	rc, _ := callDylib(symTflTensorCopyTo, []byte{i64, i64, i64}, []byte{i32}, tensor, buf, byteSize)
	if int32(rc) != 0 {
		return nil, nil, fmt.Errorf("TfLiteTensorCopyToBuffer failed")
	}
	raw := make([]byte, int(byteSize))
	if err := syscall.DylibReadMem(tflLib, buf, raw); err != nil {
		return nil, nil, fmt.Errorf("DylibReadMem failed: %w", err)
	}

	numFloats := int(byteSize / 4)
	data := make([]float32, numFloats)
	for i := range data {
		bits := uint32(raw[i*4]) | uint32(raw[i*4+1])<<8 | uint32(raw[i*4+2])<<16 | uint32(raw[i*4+3])<<24
		data[i] = math.Float32frombits(bits)
	}

	ndims, _ := callDylib(symTflTensorNumDims, []byte{i64}, []byte{i32}, tensor)
	shape := make([]int32, int(ndims))
	for i := int32(0); i < int32(ndims); i++ {
		d, _ := callDylib(symTflTensorDim, []byte{i64, i32}, []byte{i32}, tensor, uint64(i))
		shape[i] = int32(d)
	}
	return data, shape, nil
}
