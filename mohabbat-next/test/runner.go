package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"mohabbat/mohabbat"
)

type goModule struct {
	Path       string
	NativeGOOS []string
	WasmTest   bool
	Mod        string
}

type rustPackage struct {
	Name string
	Path string
}

type testRun struct {
	root         string
	logDir       string
	hostPath     string
	idleTimeout  time.Duration
	suiteTimeout time.Duration
	filter       []string
	toolchains   map[string]toolchainInfo
	sink         *eventSink
	firstError   error
}

type commandSpec struct {
	Runner string
	Target string
	Suite  string
	Dir    string
	Args   []string
	Env    []string
	Parse  func(string, string) []testEvent
}

type goJSONEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Output  string  `json:"Output"`
	Elapsed float64 `json:"Elapsed"`
}

func newTestRun(root string, idleTimeout, suiteTimeout time.Duration, filter []string) (*testRun, error) {
	if idleTimeout <= 0 || suiteTimeout <= 0 {
		return nil, fmt.Errorf("test timeouts must be positive")
	}
	runID := time.Now().UTC().Format("20060102-150405.000000000")
	logDir := filepath.Join(root, "target", "test-runs", runID)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("create test run directory %s: %w", logDir, err)
	}
	sink, err := newEventSink(logDir, runID)
	if err != nil {
		return nil, err
	}
	toolchains := probeToolchains()
	sink.setRunMetadata(filter, toolchains)
	return &testRun{
		root:         root,
		logDir:       logDir,
		hostPath:     filepath.Join(root, "target", "test-host", hostExecutableName()),
		idleTimeout:  idleTimeout,
		suiteTimeout: suiteTimeout,
		filter:       filter,
		toolchains:   toolchains,
		sink:         sink,
	}, nil
}

func probeToolchains() map[string]toolchainInfo {
	probes := []struct {
		name string
		args []string
	}{
		{name: "go", args: []string{"version"}},
		{name: "node", args: []string{"--version"}},
		{name: "zig", args: []string{"version"}},
		{name: "cargo", args: []string{"--version"}},
		{name: "rustc", args: []string{"--version"}},
	}
	result := make(map[string]toolchainInfo, len(probes))
	for _, p := range probes {
		result[p.name] = probeToolchain(p.name, p.args...)
	}
	return result
}

func probeToolchain(name string, args ...string) toolchainInfo {
	path, err := exec.LookPath(name)
	if err != nil {
		return toolchainInfo{Available: false, Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	output, err := cmd.CombinedOutput()
	version := strings.TrimSpace(string(output))
	if idx := strings.IndexByte(version, '\n'); idx >= 0 {
		version = strings.TrimSpace(version[:idx])
	}
	if err != nil {
		errMsg := err.Error()
		if version != "" {
			errMsg = fmt.Sprintf("%s: %s", errMsg, version)
		}
		return toolchainInfo{Available: false, Path: path, Error: errMsg}
	}
	return toolchainInfo{Available: true, Path: path, Version: version}
}

func (r *testRun) finish() error {
	if r.sink.summary.TestsStarted == 0 && r.firstError == nil {
		r.fail("test-selection", "no test cases were executed; check project filters and skip flags")
	}
	finishErr := r.sink.finish()
	if r.firstError != nil {
		if finishErr != nil {
			return errors.Join(r.firstError, finishErr)
		}
		return r.firstError
	}
	return finishErr
}

func (r *testRun) buildHost() {
	if runtime.GOOS != "windows" {
		r.fail("mohabbat-next-test-host", "native test host currently supports Windows only")
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.hostPath), 0o755); err != nil {
		r.fail("mohabbat-next-test-host", "create test host directory: "+err.Error())
		return
	}
	spec := commandSpec{
		Runner: "build",
		Target: "native-" + runtime.GOOS + "-" + runtime.GOARCH,
		Suite:  "Mohabbat-Next WASM test host",
		Dir:    r.root,
		Args: []string{
			"go", "run", "./mohabbat-next/build", "-runner", "-o", r.hostPath,
		},
		Parse: outputEvents("build", "native"),
	}
	r.runCommand(spec)
}

func (r *testRun) runNativeGo() {
	for _, module := range workspaceGoModules {
		if !supportsNativeGOOS(module) {
			continue
		}
		packages, err := r.listNativeTestPackages(module)
		if err != nil {
			r.fail(module.Path, "discover native Go test packages: "+err.Error())
			continue
		}
		if len(packages) == 0 {
			continue
		}
		args := []string{"go", "test", "-json", "-count=1", "-p=1"}
		if module.Mod != "" {
			args = append(args, "-mod="+module.Mod)
		}
		pkgStart := len(args)
		if r.matchesWholeModule(module.Path) {
			args = append(args, "./...")
		} else {
			for _, pkg := range packages {
				if r.matches(pkg.ImportPath) {
					args = append(args, pkg.ImportPath)
				}
			}
			if len(args) == pkgStart {
				continue
			}
		}
		r.runCommand(commandSpec{
			Runner: "go",
			Target: "native-" + runtime.GOOS + "-" + runtime.GOARCH,
			Suite:  module.Path,
			Dir:    filepath.Join(r.root, filepath.FromSlash(module.Path)),
			Args:   args,
			Parse:  parseGoJSON("go", "native-"+runtime.GOOS+"-"+runtime.GOARCH, module.Path),
		})
	}
}

func (r *testRun) runX64EmulatedGo() {
	const target = "x64-emulated-windows-amd64"
	supported, probe, symptom := supportsX64Emulated(runtime.GOOS, runtime.GOARCH)
	env := setEnvironment(os.Environ(), map[string]string{
		"GOOS":   "windows",
		"GOARCH": "amd64",
	})
	for _, module := range workspaceGoModules {
		if !supportsGOOS(module, "windows") {
			continue
		}
		packages, err := r.listTestPackages(module, env)
		if err != nil {
			if !supported {
				if r.matchesWholeModule(module.Path) || r.matches(module.Path) {
					r.disable("go", target, module.Path, probe, symptom)
				}
				continue
			}
			r.fail(module.Path, "discover x64-emulated Go test packages: "+err.Error())
			continue
		}
		if len(packages) == 0 {
			continue
		}
		if !supported {
			for _, pkg := range packages {
				r.disable("go", target, pkg.ImportPath, probe, symptom)
			}
			continue
		}
		args := []string{"go", "test", "-json", "-count=1", "-p=1"}
		if module.Mod != "" {
			args = append(args, "-mod="+module.Mod)
		}
		pkgStart := len(args)
		if r.matchesWholeModule(module.Path) {
			args = append(args, "./...")
		} else {
			for _, pkg := range packages {
				if r.matches(pkg.ImportPath) {
					args = append(args, pkg.ImportPath)
				}
			}
			if len(args) == pkgStart {
				continue
			}
		}
		r.runCommand(commandSpec{
			Runner: "go",
			Target: target,
			Suite:  module.Path,
			Dir:    filepath.Join(r.root, filepath.FromSlash(module.Path)),
			Args:   args,
			Env:    env,
			Parse:  parseGoJSON("go", target, module.Path),
		})
	}
}

func (r *testRun) runWasmGo() {
	var modules []goModule
	for _, module := range workspaceGoModules {
		if module.WasmTest {
			modules = append(modules, module)
		}
	}
	if len(modules) == 0 {
		return
	}
	var selectedPackages []wasmTestPackage
	for _, module := range modules {
		packages, err := r.listWasmTestPackages(module)
		if err != nil {
			r.fail(module.Path, "discover Rusticated Go WASM test packages: "+err.Error())
			continue
		}
		selectedPackages = append(selectedPackages, packages...)
	}
	if len(selectedPackages) == 0 {
		return
	}
	hostAvailable := false
	if runtime.GOOS == "windows" {
		if !fileExists(r.hostPath) {
			r.fail("Rusticated Go WASM", "test host is missing; run without -skip-build")
		} else {
			hostAvailable = true
		}
	}
	nodeHost := filepath.Join(r.root, "mohabbat", "washmhost", "js", "node", "index.js")
	nodeInfo := r.toolchains["node"]
	for _, pkg := range selectedPackages {
		wasmPath := filepath.Join(r.logDir, "wasm", safeName(pkg.ImportPath)+".wasm")
		if err := os.MkdirAll(filepath.Dir(wasmPath), 0o755); err != nil {
			r.fail(pkg.ImportPath, "create WASM output directory: "+err.Error())
			continue
		}
		launcherPath, err := os.Executable()
		if err != nil {
			r.fail(pkg.ImportPath, "locate test launcher executable: "+err.Error())
			continue
		}
		if err := r.runCommand(commandSpec{
			Runner: "build",
			Target: "rusticated-wasm",
			Suite:  "compile " + pkg.ImportPath,
			Dir:    r.root,
			Args:   []string{launcherPath, "--internal-build-go-test-wasm", pkg.Dir, wasmPath},
			Parse:  outputEvents("build", "rusticated-wasm"),
		}); err != nil {
			continue
		}
		if runtime.GOOS != "windows" {
			r.disable("go", "wasm-mohabbat", pkg.ImportPath, "runtime.GOOS="+runtime.GOOS, "wasm-mohabbat test host requires Windows")
		} else if hostAvailable {
			r.runCommand(commandSpec{
				Runner: "go",
				Target: "wasm-mohabbat",
				Suite:  pkg.ImportPath,
				Dir:    pkg.Dir,
				Args:   []string{"go", "tool", "test2json", "-p", pkg.ImportPath, "-t", r.hostPath, wasmPath, "-test.v=test2json"},
				Parse:  parseGoJSON("go", "wasm-mohabbat", pkg.ImportPath),
			})
		}
		if !nodeInfo.Available {
			r.disable("go", "wasm-node", pkg.ImportPath, "node --version", nodeInfo.Error)
		} else if !fileExists(nodeHost) {
			r.disable("go", "wasm-node", pkg.ImportPath, "stat mohabbat/washmhost/js/node/index.js", "missing Node WASM host")
		} else {
			r.runCommand(commandSpec{
				Runner: "go",
				Target: "wasm-node",
				Suite:  pkg.ImportPath,
				Dir:    pkg.Dir,
				Args:   []string{"go", "tool", "test2json", "-p", pkg.ImportPath, "-t", "node", nodeHost, wasmPath, "-test.v=test2json"},
				Parse:  parseGoJSON("go", "wasm-node", pkg.ImportPath),
			})
		}
	}
}

func (r *testRun) listWasmTestPackages(module goModule) ([]wasmTestPackage, error) {
	env := setEnvironment(os.Environ(), map[string]string{
		"GOOS": "wasip1", "GOARCH": "wasm", "CGO_ENABLED": "0",
	})
	return r.listTestPackages(module, env)
}

func (r *testRun) listNativeTestPackages(module goModule) ([]wasmTestPackage, error) {
	return r.listTestPackages(module, nil)
}

func (r *testRun) listTestPackages(module goModule, env []string) ([]wasmTestPackage, error) {
	moduleDir := filepath.Join(r.root, filepath.FromSlash(module.Path))
	args := []string{"list", "-e"}
	if module.Mod != "" {
		args = append(args, "-mod="+module.Mod)
	}
	args = append(args, "-f", `{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}|{{.Dir}}{{end}}`, "./...")
	cmd := exec.Command("go", args...)
	cmd.Dir = moduleDir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	var packages []wasmTestPackage
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(fields) == 2 && fields[0] != "" && fields[1] != "" {
			pkg := wasmTestPackage{ImportPath: fields[0], Dir: fields[1]}
			if r.matches(pkg.ImportPath) || r.matchesWholeModule(module.Path) {
				packages = append(packages, pkg)
			}
		}
	}
	return packages, nil
}

func supportsGOOS(module goModule, targetGOOS string) bool {
	if len(module.NativeGOOS) == 0 {
		return true
	}
	for _, goos := range module.NativeGOOS {
		if targetGOOS == goos {
			return true
		}
	}
	return false
}

func supportsNativeGOOS(module goModule) bool {
	return supportsGOOS(module, runtime.GOOS)
}

func supportsX64Emulated(goos, goarch string) (bool, string, string) {
	if goos == "windows" && goarch == "arm64" {
		return true, "", ""
	}
	return false, fmt.Sprintf("runtime.GOOS=%s runtime.GOARCH=%s", goos, goarch), "x64-emulated-windows-amd64 requires windows/arm64 host"
}

func (r *testRun) matchesWholeModule(modulePath string) bool {
	if len(r.filter) == 0 {
		return true
	}
	for _, filter := range r.filter {
		if filter == modulePath || strings.HasPrefix(modulePath, filter+"/") {
			return true
		}
	}
	return false
}

func buildGoTestWasm(packageDir, outputPath string) int {
	root, err := findWorkspaceRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := mohabbat.BuildGoTestWasm(root, packageDir, outputPath, true); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

type wasmTestPackage struct {
	ImportPath string
	Dir        string
}

func (r *testRun) runRustWasm() {
	var packages []rustPackage
	for _, pkg := range rustWorkspacePackages {
		if r.matches(pkg.Name) || r.matches(pkg.Path) {
			packages = append(packages, pkg)
		}
	}
	if len(packages) == 0 {
		return
	}
	if !fileExists(r.hostPath) {
		r.fail("Rusticated Rust WASM", "test host is missing; run without -skip-build")
		return
	}
	target := filepath.Join(r.root, "target", "rusticated-spec", "config.toml")
	for _, pkg := range packages {
		args := []string{
			"cargo", "test", "-p", pkg.Name, "--target", "wasm32-rusticated-unknown-unknown",
			"--config", target, "--config", "unstable.json-target-spec=true",
			"--config", fmt.Sprintf(
				`target.wasm32-rusticated-unknown-unknown.runner=[%s]`,
				strconv.Quote(r.hostPath),
			),
			"-Z", "unstable-options", "--message-format=json", "--", "--format=json",
		}
		r.runCommand(commandSpec{
			Runner: "rust",
			Target: "rusticated-wasm",
			Suite:  pkg.Name,
			Dir:    r.root,
			Args:   args,
			Parse:  parseRustTestOutput("rust", "rusticated-wasm", pkg.Name),
		})
	}
}

type zigTestSource struct {
	Path       string
	NativeGOOS string
	UsesPalABI bool
	UsesZwasm  bool
}

func (r *testRun) runZig() {
	zigInfo := r.toolchains["zig"]
	for _, source := range zigTestSources {
		if !r.matches(source.Path) {
			continue
		}
		targetOS := source.NativeGOOS
		if targetOS == "" {
			targetOS = runtime.GOOS
		}
		target := "native-" + targetOS + "-" + runtime.GOARCH
		if targetOS != runtime.GOOS {
			r.disable(
				"zig", target, source.Path,
				"runtime.GOOS="+targetOS,
				"the native host OS does not match this platform-specific Zig suite; Linux/WSL execution is deferred",
			)
			continue
		}
		if !zigInfo.Available {
			r.disable("zig", target, source.Path, "zig version", zigInfo.Error)
			continue
		}
		if source.UsesZwasm {
			zwasmEntry := filepath.Join(r.root, "mohabbat-next", ".build", "zig", "zwasm-overlay", "src", "zwasm.zig")
			if !fileExists(zwasmEntry) {
				r.disable("zig", target, source.Path, "stat "+zwasmEntry, "zwasm overlay not prepared; run without -skip-build")
				continue
			}
		}
		r.runCommand(commandSpec{
			Runner: "zig",
			Target: target,
			Suite:  source.Path,
			Dir:    r.root,
			Args:   zigTestArgs(r.root, source),
			Parse:  parseGoJSON("zig", target, source.Path),
		})
	}
}

func zigTestArgs(root string, source zigTestSource) []string {
	runnerPath := filepath.Join(root, "mohabbat-next", "test", "zig_test_runner.zig")
	args := []string{"zig", "test", "--test-runner", runnerPath}
	rootArg := "-Mroot=" + filepath.Join(root, filepath.FromSlash(source.Path))
	if !source.UsesPalABI && !source.UsesZwasm {
		return append(args, filepath.FromSlash(source.Path))
	}
	if source.UsesPalABI {
		args = append(args, "--dep", "pal_abi")
	}
	if source.UsesZwasm {
		args = append(args, "--dep", "zwasm")
	}
	args = append(args, rootArg)
	if source.UsesPalABI {
		args = append(args, "-Mpal_abi="+filepath.Join(root, "mohabbat-next", "pal", "abi", "guest.zig"))
	}
	if source.UsesZwasm {
		args = append(args,
			"--dep", "build_options",
			"-Mzwasm="+filepath.Join(root, "mohabbat-next", ".build", "zig", "zwasm-overlay", "src", "zwasm.zig"),
			"-Mbuild_options="+filepath.Join(root, "mohabbat-next", "runtime", "src", "build_options.zig"),
		)
	}
	return args
}

var zigTestSources = []zigTestSource{
	{Path: "mohabbat-next/pal/abi/guest.zig"},
	{Path: "mohabbat-next/pal/windows/net_ops.zig", NativeGOOS: "windows"},
	{Path: "mohabbat-next/pal/windows/platform.zig", NativeGOOS: "windows", UsesPalABI: true},
	{Path: "mohabbat-next/pal/unix/platform_linux.zig", NativeGOOS: "linux", UsesPalABI: true},
	{Path: "mohabbat-next/runtime/src/pal_bridge.zig"},
	{Path: "mohabbat-next/runtime/src/main.zig", UsesPalABI: true, UsesZwasm: true},
	{Path: "mohabbat-next/brot/unix/src/decompress.zig"},
	{Path: "mohabbat-next/brot/unix/src/pool.zig"},
	{Path: "mohabbat-next/brot/unix/src/main.zig"},
}

var nodeTestRoots = []string{
	"mohabbat/washmhost/js/node",
	"kabibi/vscode",
}

func (r *testRun) runNode() {
	files, err := r.discoverNodeTestFiles()
	if err != nil {
		r.fail("node", "discover Node test files: "+err.Error())
		return
	}
	var selected []string
	for _, relPath := range files {
		if r.matches(relPath) {
			selected = append(selected, relPath)
		}
	}
	if len(selected) == 0 {
		return
	}

	target := "native-" + runtime.GOOS + "-" + runtime.GOARCH
	nodeInfo := r.toolchains["node"]
	if !nodeInfo.Available {
		for _, relPath := range selected {
			r.disable("node", target, relPath, "node --version", nodeInfo.Error)
		}
		return
	}

	for _, relPath := range selected {
		if relPath == "kabibi/vscode/test/desktop-host.integration.test.js" {
			kabibiBat := filepath.Join(r.root, "release", "dist", "kabibi.bat")
			if !fileExists(kabibiBat) {
				r.disable("node", target, relPath, "stat release/dist/kabibi.bat", "missing development vegetable")
				continue
			}
			plat, arch := nodePlatformArch()
			addonName := fmt.Sprintf("nodepty-%s-%s.node", plat, arch)
			addonPath := filepath.Join(r.root, "kabibi", "vscode", addonName)
			if !fileExists(addonPath) {
				r.disable("node", target, relPath, "stat kabibi/vscode/"+addonName, "missing native Node PTY addon")
				continue
			}
		}
		r.runCommand(commandSpec{
			Runner: "node",
			Target: target,
			Suite:  relPath,
			Dir:    r.root,
			Args:   []string{"node", "--test", "--test-reporter=tap", filepath.FromSlash(relPath)},
			Parse:  parseNodeTAP("node", target, relPath),
		})
	}
}

func (r *testRun) discoverNodeTestFiles() ([]string, error) {
	var files []string
	for _, rootRel := range nodeTestRoots {
		rootAbs := filepath.Join(r.root, filepath.FromSlash(rootRel))
		if _, err := os.Stat(rootAbs); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		err := filepath.WalkDir(rootAbs, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			name := entry.Name()
			if entry.IsDir() {
				if name == "node_modules" || (strings.HasPrefix(name, ".") && path != rootAbs) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(name, ".test.js") || strings.HasSuffix(name, ".test.mjs") {
				rel, err := filepath.Rel(r.root, path)
				if err != nil {
					return err
				}
				files = append(files, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func nodePlatformArch() (string, string) {
	platform := runtime.GOOS
	if platform == "windows" {
		platform = "win32"
	}
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x64"
	case "386":
		arch = "ia32"
	}
	return platform, arch
}

func (r *testRun) runCommand(spec commandSpec) error {
	err := runStreamingCommand(r, spec)
	if err != nil {
		if spec.Runner == "build" {
			r.fail(spec.Suite, err.Error())
			return err
		}
		event := testEvent{
			Kind: "fail", Runner: spec.Runner, Target: spec.Target,
			Suite: spec.Suite, Name: "(runner)", Detail: err.Error(),
		}
		if emitErr := r.sink.emit(event); emitErr != nil {
			r.firstError = errors.Join(r.firstError, emitErr)
		}
		r.firstError = errors.Join(r.firstError, fmt.Errorf("%s: %w", spec.Suite, err))
	}
	return err
}

func (r *testRun) disable(runner, target, name, probe, symptom string) {
	detail := strings.TrimSpace(probe)
	if symptom = strings.TrimSpace(symptom); symptom != "" {
		if detail != "" {
			detail += ": " + symptom
		} else {
			detail = symptom
		}
	}
	event := testEvent{
		Kind:   "disabled",
		Runner: runner,
		Target: target,
		Suite:  name,
		Name:   name,
		Detail: detail,
	}
	if err := r.sink.emit(event); err != nil {
		r.firstError = errors.Join(r.firstError, err)
	}
}

func (r *testRun) fail(suite, detail string) {
	event := testEvent{
		Kind: "infra_error", Runner: "launcher", Target: "native-" + runtime.GOOS + "-" + runtime.GOARCH,
		Suite: suite, Detail: detail,
	}
	if err := r.sink.emit(event); err != nil {
		r.firstError = errors.Join(r.firstError, err)
	}
	if r.firstError == nil {
		r.firstError = fmt.Errorf("%s: %s", suite, detail)
	}
}

func (r *testRun) matches(name string) bool {
	if len(r.filter) == 0 {
		return true
	}
	name = filepath.ToSlash(name)
	for _, filter := range r.filter {
		filter = strings.TrimSuffix(filter, "/")
		if name == filter || strings.HasPrefix(name, filter+"/") ||
			strings.HasSuffix(name, "/"+filter) || strings.Contains(name, "/"+filter+"/") {
			return true
		}
	}
	return false
}

var workspaceGoModules = []goModule{
	{Path: ".", WasmTest: true},
	{Path: "mohabbat-next/wasm-abi-test", WasmTest: true},
	{Path: "ai", WasmTest: true},
	{Path: "ai/perftest"},
	{Path: "dload", WasmTest: true},
	{Path: "kabibi", WasmTest: true},
	{Path: "demo-go", WasmTest: true},
	{Path: "demo-go/curl", WasmTest: true},
	{Path: "demo-go/server", WasmTest: true},
	{Path: "demo-go/trivial", WasmTest: true},
	{Path: "mohabbat/washmhost", NativeGOOS: []string{"linux", "darwin"}},
	{Path: "mohabbat/pty"},
	{Path: "ocr4", WasmTest: true},
	{Path: "platform", WasmTest: true},
	{Path: "pope", WasmTest: true},
	{Path: "rusticated-go/internal/filepathlite", WasmTest: true},
	{Path: "u-root", WasmTest: true, Mod: "mod"},
}

var rustWorkspacePackages = []rustPackage{
	{Name: "rusticated", Path: "rusticated-rust"},
	{Name: "demo", Path: "demo"},
	{Name: "brot", Path: "mohabbat/brot"},
	{Name: "nodepty", Path: "mohabbat/nodepty"},
	{Name: "loch", Path: "demo/loch"},
	{Name: "kabibi-rust", Path: "kabibi-rust"},
	{Name: "dload", Path: "dload"},
}

func parseGoJSON(runner, target, suite string) func(string, string) []testEvent {
	return func(stream, line string) []testEvent {
		var event goJSONEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Action == "" {
			return outputEvents(runner, target)(stream, line)
		}
		kind := ""
		switch event.Action {
		case "run":
			kind = "run"
		case "pass":
			kind = "pass"
		case "fail":
			kind = "fail"
		case "skip":
			kind = "skip"
		case "output":
			if event.Output == "" {
				return nil
			}
			return []testEvent{{Kind: "output", Runner: runner, Target: target, Suite: suite, Name: event.Test, Stream: stream, Detail: event.Output}}
		default:
			return nil
		}
		if event.Package != "" {
			suite = event.Package
		}
		if event.Test == "" {
			return nil
		}
		return []testEvent{{Kind: kind, Runner: runner, Target: target, Suite: suite, Name: event.Test, Elapsed: event.Elapsed}}
	}
}

var goRunLine = regexpGoRun()

func regexpGoRun() *regexp.Regexp {
	return regexp.MustCompile(`^=== RUN\s+(.+)$`)
}

func parseVerboseGoTest(runner, target, suite string) func(string, string) []testEvent {
	return func(stream, line string) []testEvent {
		line = strings.TrimSpace(line)
		if match := goRunLine.FindStringSubmatch(line); len(match) == 2 {
			return []testEvent{{Kind: "run", Runner: runner, Target: target, Suite: suite, Name: match[1]}}
		}
		for _, state := range []struct{ prefix, kind string }{
			{"--- PASS: ", "pass"}, {"--- FAIL: ", "fail"}, {"--- SKIP: ", "skip"},
		} {
			if strings.HasPrefix(line, state.prefix) {
				name := strings.TrimSpace(strings.TrimPrefix(line, state.prefix))
				if index := strings.IndexByte(name, ' '); index >= 0 {
					name = name[:index]
				}
				return []testEvent{{Kind: state.kind, Runner: runner, Target: target, Suite: suite, Name: name}}
			}
		}
		if line != "" {
			return outputEvents(runner, target)(stream, line)
		}
		return nil
	}
}

func parseRustTestOutput(runner, target, suite string) func(string, string) []testEvent {
	return func(stream, line string) []testEvent {
		var event struct {
			Type  string `json:"type"`
			Event string `json:"event"`
			Name  string `json:"name"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && event.Type == "test" {
			kind := map[string]string{"started": "run", "ok": "pass", "failed": "fail", "ignored": "skip"}[event.Event]
			if kind != "" {
				return []testEvent{{Kind: kind, Runner: runner, Target: target, Suite: suite, Name: event.Name}}
			}
		}
		return outputEvents(runner, target)(stream, line)
	}
}

var zigTestLine = regexp.MustCompile(`^\d+/\d+\s+(.+)\.\.\.(OK|FAIL|SKIP)$`)

func parseZigTestOutput(runner, target, suite string) func(string, string) []testEvent {
	return func(stream, line string) []testEvent {
		line = strings.TrimSpace(line)
		match := zigTestLine.FindStringSubmatch(line)
		if len(match) != 3 {
			return outputEvents(runner, target)(stream, line)
		}
		name := match[1]
		if index := strings.Index(name, ".test."); index >= 0 {
			name = name[index+len(".test."):]
		}
		kind := map[string]string{"OK": "pass", "FAIL": "fail", "SKIP": "skip"}[match[2]]
		return []testEvent{
			{Kind: "run", Runner: runner, Target: target, Suite: suite, Name: name},
			{Kind: kind, Runner: runner, Target: target, Suite: suite, Name: name},
		}
	}
}

var tapTestLine = regexp.MustCompile(`^(ok|not ok)\s+\d+\s+(?:-\s+)?(.+)$`)

func parseNodeTAP(runner, target, suite string) func(string, string) []testEvent {
	return func(stream, line string) []testEvent {
		trimmed := strings.TrimRight(line, " \t\r\n")
		match := tapTestLine.FindStringSubmatch(trimmed)
		if len(match) != 3 {
			if trimmed != "" {
				return outputEvents(runner, target)(stream, line)
			}
			return nil
		}
		status := match[1]
		name := strings.TrimSpace(match[2])
		kind := "pass"
		if status == "not ok" {
			kind = "fail"
		}
		if idx := strings.LastIndex(name, " # "); idx >= 0 {
			directive := strings.TrimSpace(name[idx+3:])
			upper := strings.ToUpper(directive)
			if upper == "SKIP" || strings.HasPrefix(upper, "SKIP ") {
				kind = "skip"
				name = strings.TrimSpace(name[:idx])
			} else if upper == "TODO" || strings.HasPrefix(upper, "TODO ") {
				kind = "skip"
				name = strings.TrimSpace(name[:idx])
			}
		}
		return []testEvent{
			{Kind: "run", Runner: runner, Target: target, Suite: suite, Name: name},
			{Kind: kind, Runner: runner, Target: target, Suite: suite, Name: name},
		}
	}
}

func outputEvents(runner, target string) func(string, string) []testEvent {
	return func(stream, line string) []testEvent {
		return []testEvent{{Kind: "output", Runner: runner, Target: target, Stream: stream, Detail: line + "\n"}}
	}
}

func runStreamingCommand(run *testRun, spec commandSpec) error {
	if err := run.sink.emit(testEvent{Kind: "run", Runner: spec.Runner, Target: spec.Target, Suite: spec.Suite, Detail: strings.Join(spec.Args, " ")}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), run.suiteTimeout)
	defer cancel()
	cmd := exec.Command(spec.Args[0], spec.Args[1:]...)
	cmd.Dir = spec.Dir
	if spec.Env != nil {
		cmd.Env = spec.Env
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open stderr pipe: %w", err)
	}
	if err := configureProcess(cmd); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", spec.Args[0], err)
	}
	terminate, release, err := attachProcess(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("attach process-tree guard: %w", err)
	}
	defer release()

	base := safeName(spec.Runner + "-" + spec.Target + "-" + spec.Suite)
	stdoutLogPath := filepath.Join(run.logDir, base+".stdout.log")
	stderrLogPath := filepath.Join(run.logDir, base+".stderr.log")
	stdoutLog, err := os.Create(stdoutLogPath)
	if err != nil {
		_ = terminate()
		_ = cmd.Wait()
		return fmt.Errorf("create stdout log: %w", err)
	}
	defer stdoutLog.Close()
	stderrLog, err := os.Create(stderrLogPath)
	if err != nil {
		_ = terminate()
		_ = cmd.Wait()
		return fmt.Errorf("create stderr log: %w", err)
	}
	defer stderrLog.Close()

	type chunk struct {
		stream string
		data   []byte
		err    error
	}
	chunks := make(chan chunk, 32)
	readPipe := func(stream string, reader io.Reader) {
		buffer := make([]byte, 4096)
		for {
			n, readErr := reader.Read(buffer)
			if n > 0 {
				copyOfData := append([]byte(nil), buffer[:n]...)
				chunks <- chunk{stream: stream, data: copyOfData}
			}
			if readErr != nil {
				chunks <- chunk{stream: stream, err: readErr}
				return
			}
		}
	}
	go readPipe("stdout", stdout)
	go readPipe("stderr", stderr)

	partials := map[string][]byte{"stdout": nil, "stderr": nil}
	doneStreams := 0
	timer := time.NewTimer(run.idleTimeout)
	defer timer.Stop()
	var terminalErr error
	idleTimer := timer.C
	contextDone := ctx.Done()
	for doneStreams < 2 {
		select {
		case item := <-chunks:
			if item.err != nil {
				if !errors.Is(item.err, io.EOF) {
					terminalErr = errors.Join(terminalErr, fmt.Errorf("read %s: %w", item.stream, item.err))
				}
				doneStreams++
				if len(partials[item.stream]) > 0 {
					if err := run.processLine(spec, item.stream, string(partials[item.stream])); err != nil {
						terminalErr = errors.Join(terminalErr, err)
					}
					partials[item.stream] = nil
				}
				continue
			}
			if len(item.data) == 0 {
				continue
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(run.idleTimeout)
			writer := stdoutLog
			if item.stream == "stderr" {
				writer = stderrLog
			}
			if _, err := writer.Write(item.data); err != nil {
				terminalErr = errors.Join(terminalErr, fmt.Errorf("write %s log: %w", item.stream, err))
			}
			combined := append(partials[item.stream], item.data...)
			lines := bytes.Split(combined, []byte{'\n'})
			for _, line := range lines[:len(lines)-1] {
				if err := run.processLine(spec, item.stream, strings.TrimSuffix(string(line), "\r")); err != nil {
					terminalErr = errors.Join(terminalErr, err)
				}
			}
			partials[item.stream] = append(partials[item.stream][:0], lines[len(lines)-1]...)
		case <-idleTimer:
			terminalErr = fmt.Errorf("no stdout or stderr activity for %s", run.idleTimeout)
			terminalErr = errors.Join(terminalErr, run.sink.emit(testEvent{Kind: "infra_error", Runner: spec.Runner, Target: spec.Target, Suite: spec.Suite, Detail: terminalErr.Error()}))
			terminalErr = errors.Join(terminalErr, terminate())
			idleTimer = nil
			contextDone = nil
		case <-contextDone:
			terminalErr = fmt.Errorf("suite exceeded %s: %w", run.suiteTimeout, ctx.Err())
			terminalErr = errors.Join(terminalErr, run.sink.emit(testEvent{Kind: "infra_error", Runner: spec.Runner, Target: spec.Target, Suite: spec.Suite, Detail: terminalErr.Error()}))
			terminalErr = errors.Join(terminalErr, terminate())
			contextDone = nil
			idleTimer = nil
		}
	}

	waitErr := cmd.Wait()
	if terminalErr != nil {
		return fmt.Errorf("%w; logs: stdout=%s stderr=%s", errors.Join(terminalErr, waitErr), stdoutLogPath, stderrLogPath)
	}
	if waitErr != nil {
		return fmt.Errorf("command failed: %w; logs: stdout=%s stderr=%s", waitErr, stdoutLogPath, stderrLogPath)
	}
	return nil
}

func (r *testRun) processLine(spec commandSpec, stream, line string) error {
	parser := spec.Parse
	if parser == nil {
		parser = outputEvents(spec.Runner, spec.Target)
	}
	var emitErr error
	for _, event := range parser(stream, line) {
		if event.Runner == "" {
			event.Runner = spec.Runner
		}
		if event.Target == "" {
			event.Target = spec.Target
		}
		if event.Suite == "" {
			event.Suite = spec.Suite
		}
		emitErr = errors.Join(emitErr, r.sink.emit(event))
	}
	return emitErr
}

func setEnvironment(env []string, values map[string]string) []string {
	result := append([]string(nil), env...)
	for name, value := range values {
		prefix := name + "="
		found := false
		for i, entry := range result {
			if strings.HasPrefix(strings.ToUpper(entry), strings.ToUpper(prefix)) {
				result[i] = prefix + value
				found = true
				break
			}
		}
		if !found {
			result = append(result, prefix+value)
		}
	}
	return result
}

func safeName(value string) string {
	var result strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '-', char == '_':
			result.WriteRune(char)
		default:
			result.WriteByte('_')
		}
	}
	if result.Len() == 0 {
		return "suite"
	}
	return result.String()
}

func parseGoOutputForTest(line string) (goJSONEvent, bool) {
	var event goJSONEvent
	err := json.Unmarshal([]byte(line), &event)
	return event, err == nil && event.Action != ""
}
