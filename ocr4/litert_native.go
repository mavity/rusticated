//go:build !wasip1

package main

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// TFLite C API — stable across TFLite/LiteRT versions.
var (
	tflModelCreateFromFile          func(modelPath uintptr) uintptr
	tflModelDelete                  func(model uintptr)
	tflOptionsCreate                func() uintptr
	tflOptionsDelete                func(options uintptr)
	tflOptionsSetNumThreads         func(options uintptr, n int32)
	tflInterpreterCreate            func(model, options uintptr) uintptr
	tflInterpreterDelete            func(interp uintptr)
	tflInterpreterAllocate          func(interp uintptr) int32
	tflInterpreterInvoke            func(interp uintptr) int32
	tflGetInputTensor               func(interp uintptr, index int32) uintptr
	tflGetOutputTensor              func(interp uintptr, index int32) uintptr
	tflTensorCopyFromBuffer         func(tensor, data, size uintptr) int32
	tflTensorCopyToBuffer           func(tensor, data, size uintptr) int32
	tflTensorByteSize               func(tensor uintptr) uintptr
	tflTensorNumDims                func(tensor uintptr) int32
	tflTensorDim                    func(tensor uintptr, index int32) int32
	tflTensorType                   func(tensor uintptr) int32 // Returns TfLiteType
	tflInterpreterResizeInputTensor func(interp uintptr, inputIndex int32, inputDims uintptr, inputDimsSize uintptr) int32
)

var (
	tflLibOnce sync.Once
	tflLibErr  error
	tflLib     uintptr
)

func ensureTFLiteLoaded(libPath string) error {
	tflLibOnce.Do(func() {
		var err error
		tflLib, err = dlopen(libPath, RTLD_NOW|RTLD_GLOBAL)
		if err != nil {
			tflLibErr = fmt.Errorf("failed to load TFLite library %s: %w", libPath, err)
			return
		}
		purego.RegisterLibFunc(&tflModelCreateFromFile, tflLib, "TfLiteModelCreateFromFile")
		purego.RegisterLibFunc(&tflModelDelete, tflLib, "TfLiteModelDelete")
		purego.RegisterLibFunc(&tflOptionsCreate, tflLib, "TfLiteInterpreterOptionsCreate")
		purego.RegisterLibFunc(&tflOptionsDelete, tflLib, "TfLiteInterpreterOptionsDelete")
		purego.RegisterLibFunc(&tflOptionsSetNumThreads, tflLib, "TfLiteInterpreterOptionsSetNumThreads")
		purego.RegisterLibFunc(&tflInterpreterCreate, tflLib, "TfLiteInterpreterCreate")
		purego.RegisterLibFunc(&tflInterpreterDelete, tflLib, "TfLiteInterpreterDelete")
		purego.RegisterLibFunc(&tflInterpreterAllocate, tflLib, "TfLiteInterpreterAllocateTensors")
		purego.RegisterLibFunc(&tflInterpreterInvoke, tflLib, "TfLiteInterpreterInvoke")
		purego.RegisterLibFunc(&tflGetInputTensor, tflLib, "TfLiteInterpreterGetInputTensor")
		purego.RegisterLibFunc(&tflGetOutputTensor, tflLib, "TfLiteInterpreterGetOutputTensor")
		purego.RegisterLibFunc(&tflTensorCopyFromBuffer, tflLib, "TfLiteTensorCopyFromBuffer")
		purego.RegisterLibFunc(&tflTensorCopyToBuffer, tflLib, "TfLiteTensorCopyToBuffer")
		purego.RegisterLibFunc(&tflTensorByteSize, tflLib, "TfLiteTensorByteSize")
		purego.RegisterLibFunc(&tflTensorNumDims, tflLib, "TfLiteTensorNumDims")
		purego.RegisterLibFunc(&tflTensorDim, tflLib, "TfLiteTensorDim")
		purego.RegisterLibFunc(&tflTensorType, tflLib, "TfLiteTensorType")
		purego.RegisterLibFunc(&tflInterpreterResizeInputTensor, tflLib, "TfLiteInterpreterResizeInputTensor")
	})
	return tflLibErr
}

// TFLiteInterpreter wraps a TFLite interpreter session for one model.
type TFLiteInterpreter struct {
	ptr uintptr
}

func NewTFLiteInterpreter(libPath, modelPath string) (*TFLiteInterpreter, error) {
	if err := ensureTFLiteLoaded(libPath); err != nil {
		return nil, err
	}

	cPath, keepPath := goStringToCPtr(modelPath)
	_ = keepPath

	modelPtr := tflModelCreateFromFile(cPath)
	if modelPtr == 0 {
		return nil, fmt.Errorf("TfLiteModelCreateFromFile failed for %s", modelPath)
	}

	opts := tflOptionsCreate()
	if opts != 0 {
		tflOptionsSetNumThreads(opts, int32(runtime.NumCPU()))
	}

	interp := tflInterpreterCreate(modelPtr, opts)
	tflModelDelete(modelPtr)
	if opts != 0 {
		tflOptionsDelete(opts)
	}
	if interp == 0 {
		return nil, fmt.Errorf("TfLiteInterpreterCreate failed for %s", modelPath)
	}
	if rc := tflInterpreterAllocate(interp); rc != 0 {
		tflInterpreterDelete(interp)
		return nil, fmt.Errorf("TfLiteInterpreterAllocateTensors failed: %d", rc)
	}
	return &TFLiteInterpreter{ptr: interp}, nil
}

func (t *TFLiteInterpreter) Close() {
	if t.ptr != 0 {
		tflInterpreterDelete(t.ptr)
		t.ptr = 0
	}
}

func (t *TFLiteInterpreter) SetInput(index int, data []float32) error {
	tensor := tflGetInputTensor(t.ptr, int32(index))
	if tensor == 0 {
		return fmt.Errorf("input tensor %d is nil", index)
	}
	// Log tensor info for debugging
	dtype := tflTensorType(tensor)
	ndims := tflTensorNumDims(tensor)
	fmt.Printf("DEBUG SetInput: tensor type=%d, ndims=%d, data size=%d bytes\n", dtype, ndims, len(data)*4)

	// Print tensor dims
	for i := int32(0); i < ndims; i++ {
		dim := tflTensorDim(tensor, i)
		fmt.Printf("  dim[%d]=%d\n", i, dim)
	}

	byteSize := uintptr(len(data) * 4)
	var pinner runtime.Pinner
	pinner.Pin(&data[0])
	defer pinner.Unpin()
	rc := tflTensorCopyFromBuffer(tensor, uintptr(unsafe.Pointer(&data[0])), byteSize)
	if rc != 0 {
		// Try converting to uint8 if float32 fails
		// TfLiteType: 0=float32, 1=int32, 2=uint8, ...
		if dtype == 2 { // uint8
			return t.setInputUint8(index, data)
		}
		return fmt.Errorf("TfLiteTensorCopyFromBuffer failed: %d (dtype=%d)", rc, dtype)
	}
	return nil
}

func (t *TFLiteInterpreter) setInputUint8(index int, data []float32) error {
	tensor := tflGetInputTensor(t.ptr, int32(index))
	if tensor == 0 {
		return fmt.Errorf("input tensor %d is nil", index)
	}
	// Convert float32 [0,1] to uint8 [0,255]
	uint8Data := make([]uint8, len(data))
	for i, f := range data {
		// Assuming data is already in range [0, 1]
		// Scale to 0-255
		v := int(f * 255)
		if v > 255 {
			v = 255
		} else if v < 0 {
			v = 0
		}
		uint8Data[i] = uint8(v)
	}
	byteSize := uintptr(len(uint8Data))
	var pinner runtime.Pinner
	pinner.Pin(&uint8Data[0])
	defer pinner.Unpin()
	rc := tflTensorCopyFromBuffer(tensor, uintptr(unsafe.Pointer(&uint8Data[0])), byteSize)
	if rc != 0 {
		return fmt.Errorf("TfLiteTensorCopyFromBuffer (uint8 retry) failed: %d", rc)
	}
	return nil
}

func (t *TFLiteInterpreter) Invoke() error {
	if rc := tflInterpreterInvoke(t.ptr); rc != 0 {
		return fmt.Errorf("TfLiteInterpreterInvoke failed: %d", rc)
	}
	return nil
}

func (t *TFLiteInterpreter) GetOutput(index int) ([]float32, []int32, error) {
	tensor := tflGetOutputTensor(t.ptr, int32(index))
	if tensor == 0 {
		return nil, nil, fmt.Errorf("output tensor %d is nil", index)
	}
	byteSize := tflTensorByteSize(tensor)
	data := make([]float32, byteSize/4)

	var pinner runtime.Pinner
	pinner.Pin(&data[0])
	defer pinner.Unpin()
	rc := tflTensorCopyToBuffer(tensor, uintptr(unsafe.Pointer(&data[0])), byteSize)
	if rc != 0 {
		return nil, nil, fmt.Errorf("TfLiteTensorCopyToBuffer failed: %d", rc)
	}

	ndims := tflTensorNumDims(tensor)
	shape := make([]int32, ndims)
	for i := int32(0); i < ndims; i++ {
		shape[i] = tflTensorDim(tensor, i)
	}
	return data, shape, nil
}
