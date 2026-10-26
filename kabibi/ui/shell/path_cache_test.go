package shell

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"mvdan.cc/sh/v3/expand"
)

func TestBuildPathCacheSnapshotIndexesExecutablesScriptsAndWindowsExtensions(t *testing.T) {
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

	if runtime.GOOS != "windows" {
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
		if _, ok := unixSnapshot.scripts["local-script"]; !ok {
			t.Fatal("current-directory shebang script was not indexed")
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
}

func TestClassifyInputForDisplayUsesPATHSnapshotOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix executable permissions")
	}

	execSession, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("NewExecutionSession: %v", err)
	}
	defer execSession.Close()

	pathDir := t.TempDir()
	name := "kabibi-cache-probe-unique"
	path := filepath.Join(pathDir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	execSession.runMu.Lock()
	execSession.runner.Vars["PATH"] = expand.Variable{
		Set:      true,
		Exported: true,
		Kind:     expand.String,
		Str:      pathDir,
	}
	execSession.runMu.Unlock()

	env := execSession.EnvMap()
	cacheKey := pathCacheKey{
		cwd:  execSession.Dir(),
		path: env["PATH"],
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
	if runtime.GOOS == "windows" {
		name = "cached-command.EXE"
		pathExt = ".EXE"
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	key := pathCacheKey{cwd: root, path: root, pathExt: pathExt, windowsLike: runtime.GOOS == "windows"}
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
