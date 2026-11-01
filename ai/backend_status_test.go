package ai

import (
	"errors"
	"strings"
	"testing"
)

type backendOpenStatusTestDriver struct {
	LMDriver
	specSupported     bool
	specCreateErr     error
	specSetModeErr    error
	specSettingsErr   error
	logLevelErr       error
	logLevel          int32
	specMode          int32
	lastString        string
	dispatchDir       string
	ringbufferSets    int
	gpuDecodeSets     int
	threadCountSets   int
	specConfigCreates int
	specConfigDeletes int
	settingsDeletes   int
	modelFileDeletes  int
	engineCreates     int
}

func (*backendOpenStatusTestDriver) ensureLoaded(string) error { return nil }
func (d *backendOpenStatusTestDriver) stringToCPtr(value string) (uint64, func(), error) {
	d.lastString = value
	return 1, func() {}, nil
}
func (d *backendOpenStatusTestDriver) setLogLevel(level int32) error {
	d.logLevel = level
	return d.logLevelErr
}
func (*backendOpenStatusTestDriver) loadedFileCreate(uint64) (uint64, error) {
	return 2, nil
}
func (d *backendOpenStatusTestDriver) loadedFileDelete(uint64) { d.modelFileDeletes++ }
func (d *backendOpenStatusTestDriver) loadedFileHasSpeculativeDecodingSupport(uint64) (bool, error) {
	return d.specSupported, nil
}
func (*backendOpenStatusTestDriver) settingsCreate(uint64, uint64, uint64, uint64) (uint64, error) {
	return 3, nil
}
func (d *backendOpenStatusTestDriver) settingsDelete(uint64) { d.settingsDeletes++ }
func (d *backendOpenStatusTestDriver) speculativeDecodingConfigCreate() (uint64, error) {
	d.specConfigCreates++
	return 4, d.specCreateErr
}
func (d *backendOpenStatusTestDriver) speculativeDecodingConfigDelete(uint64) {
	d.specConfigDeletes++
}
func (d *backendOpenStatusTestDriver) speculativeDecodingConfigSetMode(_ uint64, mode int32) error {
	d.specMode = mode
	return d.specSetModeErr
}
func (d *backendOpenStatusTestDriver) settingsSetSpeculativeDecodingConfig(uint64, uint64) error {
	return d.specSettingsErr
}
func (d *backendOpenStatusTestDriver) settingsSetLitertDispatchLibDir(uint64, uint64) error {
	d.dispatchDir = d.lastString
	return nil
}
func (d *backendOpenStatusTestDriver) settingsSetUseRingbuffersLocalAttention(uint64, bool) error {
	d.ringbufferSets++
	return nil
}
func (d *backendOpenStatusTestDriver) settingsSetGpuDecodeStepsPerSync(uint64, int32) error {
	d.gpuDecodeSets++
	return nil
}
func (d *backendOpenStatusTestDriver) settingsSetNumThreads(uint64, int32) error {
	d.threadCountSets++
	return nil
}
func (d *backendOpenStatusTestDriver) engineCreate(uint64) (uint64, error) {
	d.engineCreates++
	return 5, nil
}
func (*backendOpenStatusTestDriver) engineDelete(uint64) {}

func TestSharedOpenBackendOwnsSpeculativeConfigUntilCopied(t *testing.T) {
	driver := &backendOpenStatusTestDriver{specSupported: true}
	engine, err := sharedOpenBackend(driver, "library", "model", "cpu", 0)
	if err != nil {
		t.Fatalf("sharedOpenBackend() error = %v", err)
	}
	if driver.specConfigCreates != 1 || driver.specConfigDeletes != 1 || driver.specMode != 1 {
		t.Fatalf("speculative config lifecycle = creates:%d deletes:%d mode:%d; want 1/1/1",
			driver.specConfigCreates, driver.specConfigDeletes, driver.specMode)
	}
	if driver.modelFileDeletes != 1 || driver.settingsDeletes != 1 || driver.engineCreates != 1 {
		t.Fatalf("resource lifecycle = model deletes:%d settings deletes:%d engine creates:%d; want 1/1/1",
			driver.modelFileDeletes, driver.settingsDeletes, driver.engineCreates)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("engine.Close() error = %v", err)
	}
}

func TestSharedOpenBackendPropagatesSpeculativeConfigStatus(t *testing.T) {
	statusErr := newLiteRtStatusError("set speculative config", 3, "invalid config")
	driver := &backendOpenStatusTestDriver{
		specSupported:   true,
		specSettingsErr: statusErr,
	}
	engine, err := sharedOpenBackend(driver, "library", "model", "cpu", 0)
	if !errors.Is(err, statusErr) {
		t.Fatalf("sharedOpenBackend() error = %v, want speculative config error", err)
	}
	if engine != nil || driver.engineCreates != 0 {
		t.Fatalf("sharedOpenBackend() = (%v, %v), engine creations %d; want no engine",
			engine, err, driver.engineCreates)
	}
	if driver.specConfigDeletes != 1 || driver.settingsDeletes != 1 {
		t.Fatalf("cleanup counts = speculative config %d, settings %d; want 1/1",
			driver.specConfigDeletes, driver.settingsDeletes)
	}
}

func TestSharedOpenBackendConfiguresWindowsNPUDispatchDirectory(t *testing.T) {
	driver := &backendOpenStatusTestDriver{}
	engine, err := sharedOpenBackend(driver, `C:\cache\litert_lm\litert-lm.dll`, "model", "npu", 0)
	if err != nil {
		t.Fatalf("sharedOpenBackend() error = %v", err)
	}
	if want := "C:/cache/litert_lm/vendors/intel_openvino/dispatch"; driver.dispatchDir != want {
		t.Fatalf("NPU dispatch directory = %q, want %q", driver.dispatchDir, want)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("engine.Close() error = %v", err)
	}
}

func TestSharedOpenBackendUsesBackendCompatibleSettings(t *testing.T) {
	tests := []struct {
		backend       string
		ringbuffers   int
		gpuDecodeSync int
		threadCount   int
	}{
		{backend: "cpu", threadCount: 1},
		{backend: "gpu", gpuDecodeSync: 1},
		{backend: "npu"},
		{backend: "gpu_artisan", ringbuffers: 1},
	}
	for _, test := range tests {
		t.Run(test.backend, func(t *testing.T) {
			driver := &backendOpenStatusTestDriver{}
			engine, err := sharedOpenBackend(driver, "library", "model", test.backend, 0)
			if err != nil {
				t.Fatalf("sharedOpenBackend() error = %v", err)
			}
			if driver.ringbufferSets != test.ringbuffers ||
				driver.gpuDecodeSets != test.gpuDecodeSync ||
				driver.threadCountSets != test.threadCount {
				t.Fatalf("backend settings = ringbuffers:%d GPU-sync:%d threads:%d; want %d/%d/%d",
					driver.ringbufferSets, driver.gpuDecodeSets, driver.threadCountSets,
					test.ringbuffers, test.gpuDecodeSync, test.threadCount)
			}
			if err := engine.Close(); err != nil {
				t.Fatalf("engine.Close() error = %v", err)
			}
		})
	}
}

func TestConfigureLMLoggingUsesValidSeverityAndPropagatesFailure(t *testing.T) {
	for _, test := range []struct {
		verbose bool
		want    int32
	}{
		{verbose: false, want: 3},
		{verbose: true, want: 0},
	} {
		oldVerbose := Verbose
		Verbose = test.verbose
		driver := &backendOpenStatusTestDriver{}
		err := configureLMLogging(driver)
		Verbose = oldVerbose
		if err != nil || driver.logLevel != test.want {
			t.Fatalf("configureLMLogging(verbose=%t) = %v, level %d; want nil, %d",
				test.verbose, err, driver.logLevel, test.want)
		}
	}

	statusErr := newLiteRtStatusError("set log level", 3, "invalid severity")
	driver := &backendOpenStatusTestDriver{logLevelErr: statusErr}
	if err := configureLMLogging(driver); !errors.Is(err, statusErr) {
		t.Fatalf("configureLMLogging() error = %v, want wrapped status error", err)
	}
}

func TestRuntimeLibraryLoadErrorExplainsLinuxABIRequirement(t *testing.T) {
	err := runtimeLibraryLoadError("liblitert-lm.so", "linux", errors.New("libc.so.6: version `GLIBC_2.27' not found"))
	if !strings.Contains(err.Error(), "glibc 2.27 or newer") ||
		!strings.Contains(err.Error(), "not compatible with musl") {
		t.Fatalf("runtime load error = %q, want glibc and musl guidance", err)
	}
}

type embeddingCleanupTestDriver struct {
	LMDriver
	inputErr        error
	inputCreates    int
	inputDeletes    int
	optionsDeletes  int
	settingsDeletes int
	engineDeletes   int
}

func (*embeddingCleanupTestDriver) ensureLoaded(string) error { return nil }
func (*embeddingCleanupTestDriver) setLogLevel(int32) error   { return nil }
func (*embeddingCleanupTestDriver) stringToCPtr(string) (uint64, func(), error) {
	return 1, func() {}, nil
}
func (*embeddingCleanupTestDriver) embeddingSettingsCreate(uint64, uint64, uint64, uint64) (uint64, error) {
	return 2, nil
}
func (d *embeddingCleanupTestDriver) embeddingSettingsDelete(uint64) { d.settingsDeletes++ }
func (*embeddingCleanupTestDriver) embeddingSettingsSetNumThreads(uint64, int32) error {
	return nil
}
func (*embeddingCleanupTestDriver) embeddingEngineCreate(uint64) (uint64, error) {
	return 3, nil
}
func (d *embeddingCleanupTestDriver) embeddingEngineDelete(uint64) { d.engineDeletes++ }
func (*embeddingCleanupTestDriver) embeddingOptionsCreate() (uint64, error) {
	return 4, nil
}
func (d *embeddingCleanupTestDriver) embeddingOptionsDelete(uint64) { d.optionsDeletes++ }
func (*embeddingCleanupTestDriver) embeddingOptionsSetNormalize(uint64, bool) error {
	return nil
}
func (*embeddingCleanupTestDriver) embeddingOptionsSetInputOverflowStrategy(uint64, int32) error {
	return nil
}
func (d *embeddingCleanupTestDriver) inputDataCreate(int32, []byte) (uint64, error) {
	d.inputCreates++
	if d.inputCreates == 2 {
		return 0, d.inputErr
	}
	return 5, nil
}
func (d *embeddingCleanupTestDriver) inputDataDelete(uint64) { d.inputDeletes++ }

func TestEmbedderCleansUpPartialInputCreation(t *testing.T) {
	inputErr := errors.New("second input failed")
	driver := &embeddingCleanupTestDriver{inputErr: inputErr}
	embedder, err := sharedOpenEmbedder(driver, "library", "model", "cpu")
	if err != nil {
		t.Fatalf("sharedOpenEmbedder() error = %v", err)
	}
	_, err = embedder.ComputeEmbedding([]embedInput{
		{dataType: inputDataTypeText, data: []byte("first")},
		{dataType: inputDataTypeText, data: []byte("second")},
	})
	if !errors.Is(err, inputErr) {
		t.Fatalf("ComputeEmbedding() error = %v, want input creation error", err)
	}
	if driver.inputCreates != 2 || driver.inputDeletes != 1 || driver.optionsDeletes != 1 {
		t.Fatalf("partial embedding cleanup = input creates:%d input deletes:%d options deletes:%d; want 2/1/1",
			driver.inputCreates, driver.inputDeletes, driver.optionsDeletes)
	}
	if driver.settingsDeletes != 1 {
		t.Fatalf("embedding settings deletes = %d, want 1", driver.settingsDeletes)
	}
	if err := embedder.Close(); err != nil {
		t.Fatalf("embedder.Close() error = %v", err)
	}
	if driver.engineDeletes != 1 {
		t.Fatalf("embedding engine deletes = %d, want 1", driver.engineDeletes)
	}
}
