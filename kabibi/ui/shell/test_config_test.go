package shell

import (
	"testing"

	"github.com/mavity/rusticated/kabibi/config"
)

func setTestConfigDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	restore := config.SetDirectoryForTesting(dir)
	t.Cleanup(restore)
	return dir
}
