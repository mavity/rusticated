package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func toWindowsPath(p string) string {
	if runtime.GOOS == "windows" {
		return p
	}
	if out, err := exec.Command("wslpath", "-w", p).Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return p
}

func findWindowsCompiler() (string, error) {
	windir := os.Getenv("WINDIR")
	if windir != "" {
		candidate := filepath.Join(windir, "Microsoft.NET", "Framework64", "v4.0.30319", "csc.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		candidate32 := filepath.Join(windir, "Microsoft.NET", "Framework", "v4.0.30319", "csc.exe")
		if _, err := os.Stat(candidate32); err == nil {
			return candidate32, nil
		}
	}
	// Check common WSL mount paths
	wslCandidates := []string{
		"/mnt/c/Windows/Microsoft.NET/Framework64/v4.0.30319/csc.exe",
		"/mnt/c/Windows/Microsoft.NET/Framework/v4.0.30319/csc.exe",
	}
	for _, c := range wslCandidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}

	if path, err := exec.LookPath("csc.exe"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("csc"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf(".NET Framework C# compiler (csc.exe) not found in %%WINDIR%%, WSL mount, or PATH")
}

func findDotnetCommand() (string, error) {
	if path, err := exec.LookPath("dotnet"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("dotnet.exe"); err == nil {
		return path, nil
	}
	wslDotnet := "/mnt/c/Program Files/dotnet/dotnet.exe"
	if _, err := os.Stat(wslDotnet); err == nil {
		return wslDotnet, nil
	}
	return "", fmt.Errorf("dotnet command not found in PATH")
}

func ensureZstdSharpDll(windowsHostDir, workDir string, verbose bool) (string, error) {
	dotnetCmd, err := findDotnetCommand()
	if err != nil {
		return "", err
	}

	packerProj := filepath.Join(windowsHostDir, "PackComponents.csproj")
	packerBin := filepath.Join(workDir, "packer", "bin")
	packerObj := filepath.Join(workDir, "packer", "obj")

	projArg := packerProj
	baseOutArg := filepath.ToSlash(packerBin) + "/"
	baseIntArg := filepath.ToSlash(packerObj) + "/"

	if strings.HasSuffix(dotnetCmd, ".exe") && runtime.GOOS != "windows" {
		projArg = toWindowsPath(packerProj)
		baseOutArg = toWindowsPath(packerBin) + `\`
		baseIntArg = toWindowsPath(packerObj) + `\`
	}

	cmd := exec.Command(dotnetCmd, "build", projArg, "-c", "Release", "--nologo",
		"-p:BaseOutputPath="+baseOutArg,
		"-p:BaseIntermediateOutputPath="+baseIntArg,
	)
	cmd.Dir = windowsHostDir
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("dotnet build PackComponents.csproj: %w\n%s", err, string(out))
	}

	net462Dir := filepath.Join(packerBin, "Release", "net462")
	dllPath := filepath.Join(net462Dir, "ZstdSharp.dll")
	if _, err := os.Stat(dllPath); err != nil {
		return "", fmt.Errorf("expected ZstdSharp.dll at %s: %w", dllPath, err)
	}
	return net462Dir, nil
}

type WindowsComponentResource struct {
	FilePath     string
	ResourceName string
}

func buildWindowsHostExe(windowsHostDir, workDir, outExePath string, components []WindowsComponentResource, verbose bool) error {
	compiler, err := findWindowsCompiler()
	if err != nil {
		return err
	}

	depsDir, err := ensureZstdSharpDll(windowsHostDir, workDir, verbose)
	if err != nil {
		return fmt.Errorf("ensure ZstdSharp dependency: %w", err)
	}

	manifest := filepath.Join(windowsHostDir, "ProbeHost.manifest")
	programCs := filepath.Join(windowsHostDir, "Program.cs")

	isWindowsBinary := strings.HasSuffix(compiler, ".exe") && runtime.GOOS != "windows"

	targetOut := outExePath
	targetManifest := manifest
	targetProgramCs := programCs
	if isWindowsBinary {
		targetOut = toWindowsPath(outExePath)
		targetManifest = toWindowsPath(manifest)
		targetProgramCs = toWindowsPath(programCs)
	}

	args := []string{
		"/nologo",
		"/target:exe",
		"/platform:anycpu",
		"/out:" + targetOut,
		"/win32manifest:" + targetManifest,
		"/reference:System.dll",
	}

	// Add references and embedded resources from dependencies (ZstdSharp.dll, etc.)
	entries, err := os.ReadDir(depsDir)
	if err != nil {
		return fmt.Errorf("read deps dir %s: %w", depsDir, err)
	}

	dllCount := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".dll") {
			continue
		}
		dllCount++
		fullPath := filepath.Join(depsDir, entry.Name())
		resourceName := "MohabbatNext.Internal." + entry.Name()
		refPath := fullPath
		if isWindowsBinary {
			refPath = toWindowsPath(fullPath)
		}
		args = append(args, "/reference:"+refPath)
		args = append(args, fmt.Sprintf("/resource:%s,%s", refPath, resourceName))
	}
	if dllCount == 0 {
		return fmt.Errorf("no managed runtime dependencies found in %s", depsDir)
	}

	// Add the 4 compressed components
	for _, comp := range components {
		compPath := comp.FilePath
		if isWindowsBinary {
			compPath = toWindowsPath(comp.FilePath)
		}
		args = append(args, fmt.Sprintf("/resource:%s,%s", compPath, comp.ResourceName))
	}

	args = append(args, targetProgramCs)

	cmd := exec.Command(compiler, args...)
	cmd.Dir = windowsHostDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("compile Windows CLR host (csc.exe): %w\n%s", err, string(out))
	}

	return nil
}
