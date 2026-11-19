package shell

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai"
	"github.com/mavity/rusticated/platform"
	"mvdan.cc/sh/v3/expand"
)

func TestBuildPathCacheSnapshotIndexesPATHExecutablesAndWindowsExtensions(t *testing.T) {
	root := t.TempDir()
	pathDir := filepath.Join(root, "bin")
	cwd := filepath.Join(root, "cwd")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile := func(path string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
	}

	writeFile(filepath.Join(cwd, "local-script"), 0o644)
	writeFile(filepath.Join(pathDir, "deploy.CMD"), 0o644)
	writeFile(filepath.Join(pathDir, "not-command.txt"), 0o644)

	if platform.HostOS() != "windows" {
		writeFile(filepath.Join(pathDir, "path-command"), 0o755)
		writeFile(filepath.Join(pathDir, "not-executable"), 0o644)
		unixSnapshot := buildPathCacheSnapshot(pathCacheKey{
			cwd:  cwd,
			path: pathDir,
		})
		if _, ok := unixSnapshot.commands["path-command"]; !ok {
			t.Fatal("PATH executable was not indexed")
		}
		if _, ok := unixSnapshot.commands["not-executable"]; ok {
			t.Fatal("non-executable PATH file was indexed")
		}
		if _, ok := unixSnapshot.commands["local-script"]; ok {
			t.Fatal("current-directory file was indexed as a bare command")
		}
	}

	windowsSnapshot := buildPathCacheSnapshot(pathCacheKey{
		cwd:         cwd,
		path:        pathDir,
		pathExt:     ".COM;.EXE;.BAT;.CMD",
		windowsLike: true,
	})
	if _, ok := windowsSnapshot.commands["deploy"]; !ok {
		t.Fatal("PATHEXT command name was not indexed")
	}
	if _, ok := windowsSnapshot.commands["deploy.cmd"]; !ok {
		t.Fatal("explicit PATHEXT extension was not indexed")
	}
	if _, ok := windowsSnapshot.commands["not-command.txt"]; ok {
		t.Fatal("file outside PATHEXT was indexed as a command")
	}
}

func TestWindowsPathCacheIndexesSymlinkNamesWithoutResolving(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "store-app.exe")
	if err := os.Symlink(filepath.Join(dir, "missing-target.exe"), link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	snapshot := buildPathCacheSnapshot(pathCacheKey{
		path:        dir,
		pathExt:     ".EXE",
		windowsLike: true,
	})
	for _, name := range []string{"store-app", "store-app.exe"} {
		if _, ok := snapshot.commands[name]; !ok {
			t.Errorf("Windows PATH cache omitted symlink name %q", name)
		}
	}
}

func TestCompletionFilesystemErrorsRequireVerboseMode(t *testing.T) {
	previousVerbose := ai.Verbose
	previousOutput := log.Writer()
	previousFlags := log.Flags()
	t.Cleanup(func() {
		ai.Verbose = previousVerbose
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})

	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	err := errors.New("path lookup failed")

	ai.Verbose = false
	logUnexpectedCompletionFSError("resolve command symlink", "command.exe", err)
	if output.Len() != 0 {
		t.Fatalf("non-verbose completion log = %q, want no output", output.String())
	}

	ai.Verbose = true
	logUnexpectedCompletionFSError("resolve command symlink", "command.exe", err)
	if got := output.String(); !strings.Contains(got, "resolve command symlink") ||
		!strings.Contains(got, "path lookup failed") {
		t.Fatalf("verbose completion log = %q, want operation and error details", got)
	}
}

func TestClassifyInputForDisplayUsesPATHSnapshotOnly(t *testing.T) {
	execSession, err := NewExecutionSession("")
	if err != nil {
		t.Fatalf("NewExecutionSession: %v", err)
	}
	defer execSession.Close()
	execSession.pathCache.close()
	execSession.pathCache = newPathCommandCache()

	windowsLike := platform.HostOS() == "windows"
	pathDir := t.TempDir()
	name := "kabibi-cache-probe-unique"
	pathName := name
	pathExt := ""
	mode := os.FileMode(0o755)
	if windowsLike {
		pathName += ".CMD"
		pathExt = ".CMD"
		mode = 0o600
	}
	path := filepath.Join(pathDir, pathName)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}

	execSession.runMu.Lock()
	execSession.runner.Vars["PATH"] = expand.Variable{
		Set:      true,
		Exported: true,
		Kind:     expand.String,
		Str:      pathDir,
	}
	if windowsLike {
		execSession.runner.Vars["PATHEXT"] = expand.Variable{
			Set:      true,
			Exported: true,
			Kind:     expand.String,
			Str:      pathExt,
		}
	}
	execSession.runMu.Unlock()

	env := execSession.EnvMap()
	cacheKey := pathCacheKey{
		cwd:         execSession.Dir(),
		path:        env["PATH"],
		pathExt:     env["PATHEXT"],
		windowsLike: windowsLike,
	}
	execSession.pathCache.mu.Lock()
	execSession.pathCache.snapshot = buildPathCacheSnapshot(cacheKey)
	execSession.pathCache.mu.Unlock()

	if route, _ := execSession.ClassifyInputForDisplay(name + " --version"); route != RouteShell {
		t.Fatalf("cached command display route = %v, want RouteShell", route)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if route, _ := execSession.ClassifyInput(name + " --version"); route != RouteAI {
		t.Fatalf("authoritative route after executable removal = %v, want RouteAI", route)
	}
	if route, _ := execSession.ClassifyInputForDisplay(name + " --version"); route != RouteShell {
		t.Fatalf("display route before cache refresh = %v, want cached RouteShell", route)
	}
}

func TestPathCommandCacheRefreshesAndCloses(t *testing.T) {
	cache := newPathCommandCache()
	defer cache.close()

	root := t.TempDir()
	name := "cached-command"
	pathExt := ""
	windowsLike := platform.HostOS() == "windows"
	if windowsLike {
		name = "cached-command.EXE"
		pathExt = ".EXE"
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	key := pathCacheKey{cwd: root, path: root, pathExt: pathExt, windowsLike: windowsLike}
	cache.schedule(key, true)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cache.contains(key, "cached-command") {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("immediate PATH cache refresh did not index the executable")
}

func TestPathCommandCacheReturnsCachedCompletionNames(t *testing.T) {
	root := t.TempDir()
	name := "completion-command"
	pathExt := ""
	windowsLike := platform.HostOS() == "windows"
	if windowsLike {
		name += ".EXE"
		pathExt = ".EXE"
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	key := pathCacheKey{cwd: root, path: root, pathExt: pathExt, windowsLike: windowsLike}
	cache := newPathCommandCache()
	defer cache.close()
	cache.mu.Lock()
	cache.snapshot = buildPathCacheSnapshot(key)
	cache.mu.Unlock()

	names := cache.commandNames(key, "completion-")
	if len(names) == 0 {
		t.Fatal("fresh PATH cache returned no matching command names")
	}
}
