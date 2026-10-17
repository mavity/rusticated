package gitcore

import (
	"errors"
	"runtime/debug"
)

const goGitModule = "github.com/go-git/go-git/v5"

// Keep this in sync with the go-git requirement in Kabibi's go.mod; local
// replacements report "(devel)" in Go build information.
const goGitPinnedVersion = "v5.19.3"

func LibraryVersion() (string, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", errors.New("Go build information is unavailable")
	}
	for _, dep := range info.Deps {
		if dep.Path != goGitModule {
			continue
		}
		version := dep.Version
		if dep.Replace != nil && dep.Replace.Version != "" {
			version = dep.Replace.Version
		}
		if version == "(devel)" && dep.Replace != nil {
			return goGitPinnedVersion, nil
		}
		if version == "" || version == "(devel)" {
			return "", errors.New("go-git module version is unavailable")
		}
		return version, nil
	}
	return "", errors.New("go-git module version was not included in build information")
}
