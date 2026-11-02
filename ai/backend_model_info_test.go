package ai

import (
	"errors"
	"strings"
	"testing"
)

type modelInfoTestDriver struct {
	LMDriver
	failedGetter          string
	getterErr             error
	maxContextTokens      uint32
	isDynamicContext      bool
	logLevel              int32
	logLevelConfigured    bool
	logLevelAtFileLoading int32
}

func (*modelInfoTestDriver) ensureLoaded(string) error { return nil }
func (*modelInfoTestDriver) stringToCPtr(string) (uint64, func(), error) {
	return 1, func() {}, nil
}
func (d *modelInfoTestDriver) setLogLevel(level int32) error {
	d.logLevel = level
	d.logLevelConfigured = true
	return nil
}
func (d *modelInfoTestDriver) loadedFileCreate(uint64) (uint64, error) {
	if d.logLevelConfigured {
		d.logLevelAtFileLoading = d.logLevel
	}
	return 1, nil
}
func (*modelInfoTestDriver) loadedFileDelete(uint64) {}
func (d *modelInfoTestDriver) getterError(name string) error {
	if d.failedGetter == name {
		return d.getterErr
	}
	return nil
}
func (d *modelInfoTestDriver) loadedFileMaxContextTokens(uint64) (uint32, error) {
	return d.maxContextTokens, d.getterError("max context")
}
func (d *modelInfoTestDriver) loadedFileIsDynamicContext(uint64) (bool, error) {
	return d.isDynamicContext, d.getterError("dynamic context")
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

func TestSharedInspectModelTreatsMissingContextLimitAsOptional(t *testing.T) {
	driver := &modelInfoTestDriver{
		failedGetter: "max context",
		getterErr:    newLiteRtStatusError("max context", liteRtStatusNotFound, "model has no context limit"),
	}
	info, err := sharedInspectModel(driver, "library", "model")
	if err != nil {
		t.Fatalf("sharedInspectModel() error = %v, want nil for NotFound context limit", err)
	}
	if info == nil || info.MaxContextTokens != 0 {
		t.Fatalf("sharedInspectModel() info = %+v, want zero context limit", info)
	}
}

func TestSharedInspectModelConfiguresLoggingBeforeLoadingModel(t *testing.T) {
	oldVerbose := Verbose
	Verbose = false
	defer func() { Verbose = oldVerbose }()

	driver := &modelInfoTestDriver{}
	if _, err := sharedInspectModel(driver, "library", "model"); err != nil {
		t.Fatalf("sharedInspectModel() error = %v", err)
	}
	if driver.logLevelAtFileLoading != liteRtLmLogSeveritySilent {
		t.Fatalf("log level during model load = %d, want silent level %d",
			driver.logLevelAtFileLoading, liteRtLmLogSeveritySilent)
	}
}

func TestSharedInspectModelReportsDynamicContextDefault(t *testing.T) {
	driver := &modelInfoTestDriver{maxContextTokens: 4096, isDynamicContext: true}
	info, err := sharedInspectModel(driver, "library", "model")
	if err != nil {
		t.Fatalf("sharedInspectModel() error = %v", err)
	}
	if info == nil || info.MaxContextTokens != 4096 || !info.IsDynamicContext {
		t.Fatalf("sharedInspectModel() info = %+v, want the dynamic runtime-reported default", info)
	}
}
