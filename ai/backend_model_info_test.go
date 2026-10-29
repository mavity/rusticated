package ai

import (
	"errors"
	"strings"
	"testing"
)

type modelInfoTestDriver struct {
	LMDriver
	failedGetter string
	getterErr    error
}

func (*modelInfoTestDriver) ensureLoaded(string) error { return nil }
func (*modelInfoTestDriver) stringToCPtr(string) (uint64, func(), error) {
	return 1, func() {}, nil
}
func (*modelInfoTestDriver) loadedFileCreate(uint64) uint64 { return 1 }
func (*modelInfoTestDriver) loadedFileDelete(uint64)        {}
func (d *modelInfoTestDriver) getterError(name string) error {
	if d.failedGetter == name {
		return d.getterErr
	}
	return nil
}
func (d *modelInfoTestDriver) loadedFileMaxContextTokens(uint64) (uint32, error) {
	return 0, d.getterError("max context")
}
func (d *modelInfoTestDriver) loadedFileIsDynamicContext(uint64) (bool, error) {
	return false, d.getterError("dynamic context")
}
func (d *modelInfoTestDriver) loadedFileSupportsThinking(uint64) (bool, error) {
	return false, d.getterError("thinking")
}
func (d *modelInfoTestDriver) loadedFileSupportsFunctionCalling(uint64) (bool, error) {
	return false, d.getterError("function calling")
}
func (d *modelInfoTestDriver) loadedFileModalitySupportedBackends(uint64, int32) ([]uint32, error) {
	return nil, d.getterError("supported backends")
}

func TestSharedInspectModelPropagatesGetterErrors(t *testing.T) {
	getters := []struct {
		name       string
		errorLabel string
	}{
		{name: "max context", errorLabel: "context-token"},
		{name: "dynamic context", errorLabel: "dynamic-context"},
		{name: "thinking", errorLabel: "thinking"},
		{name: "function calling", errorLabel: "function-calling"},
		{name: "supported backends", errorLabel: "supported backends"},
	}
	for _, getter := range getters {
		t.Run(getter.name, func(t *testing.T) {
			getterErr := errors.New("getter failed")
			driver := &modelInfoTestDriver{failedGetter: getter.name, getterErr: getterErr}

			info, err := sharedInspectModel(driver, "library", "model")
			if !errors.Is(err, getterErr) {
				t.Fatalf("sharedInspectModel() error = %v, want wrapped getter error", err)
			}
			if info != nil {
				t.Fatalf("sharedInspectModel() info = %+v, want nil on failed query", info)
			}
			if !strings.Contains(err.Error(), getter.errorLabel) {
				t.Fatalf("error %q does not identify failed %q getter", err, getter.name)
			}
		})
	}
}

func TestSharedInspectModelPreservesFalseAndZeroCapabilities(t *testing.T) {
	info, err := sharedInspectModel(&modelInfoTestDriver{}, "library", "model")
	if err != nil {
		t.Fatalf("sharedInspectModel() error = %v", err)
	}
	if info == nil || info.MaxContextTokens != 0 || info.IsDynamicContext ||
		info.SupportsThinking || info.SupportsFunctionCalling || len(info.SupportedBackends) != 0 {
		t.Fatalf("sharedInspectModel() info = %+v, want actual zero/false capability values", info)
	}
}
