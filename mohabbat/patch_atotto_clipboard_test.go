package mohabbat

import (
	"go/format"
	"strings"
	"testing"
)

func TestModulePathFromGomod(t *testing.T) {
	gomod := `// a sample module
module github.com/mavity/rusticated/kabibi

go 1.27.1
`
	if got := modulePathFromGomod(gomod); got != "github.com/mavity/rusticated/kabibi" {
		t.Fatalf("modulePathFromGomod() = %q, want Kabibi module path", got)
	}
}

func TestIsKabibiClipboardDependency(t *testing.T) {
	kabibiMod := "module github.com/mavity/rusticated/kabibi\n"
	otherMod := "module github.com/example/app\n"

	if !isKabibiClipboardDependency("github.com/atotto/clipboard", kabibiMod) {
		t.Fatal("Kabibi clipboard replacement was not selected for patching")
	}
	if isKabibiClipboardDependency("github.com/atotto/clipboard", otherMod) {
		t.Fatal("clipboard patch was selected for a different project")
	}
	if isKabibiClipboardDependency("github.com/example/clipboard", kabibiMod) {
		t.Fatal("clipboard patch was selected for a different dependency")
	}
}

func TestPatchAtottoClipboardInMemory(t *testing.T) {
	files := map[string][]byte{
		"clipboard.go": []byte(`package clipboard

// Unsupported might be set true during clipboard init, to help callers decide
// whether or not to offer clipboard options.
var Unsupported bool
`),
		"clipboard_unix.go": []byte(`package clipboard

import (
	"os/exec"
)

func init() {
	if _, err := exec.LookPath("xclip"); err == nil {
		return
	}
	Unsupported = true
}

func readAll() (string, error) {
	if Unsupported {
		return "", nil
	}
	return "", nil
}

func writeAll(text string) error {
	if Unsupported {
		return nil
	}
	return nil
}
`),
	}

	if err := patchAtottoClipboardInMemory(files); err != nil {
		t.Fatalf("patch clipboard source: %v", err)
	}

	common := string(files["clipboard.go"])
	if !strings.Contains(common, "func IsUnsupported() bool") ||
		!strings.Contains(common, "initializeClipboard()") ||
		strings.Contains(common, "var Unsupported bool") {
		t.Fatalf("clipboard.go did not replace Unsupported with lazy IsUnsupported():\n%s", common)
	}

	unix := string(files["clipboard_unix.go"])
	if !strings.Contains(unix, "clipboardInitOnce.Do(initClipboardBackend)") ||
		!strings.Contains(unix, "func initClipboardBackend()") ||
		strings.Count(unix, "initializeClipboard()") != 2 ||
		strings.Contains(unix, "Unsupported") {
		t.Fatalf("clipboard_unix.go did not defer discovery to clipboard operations:\n%s", unix)
	}
	initStart := strings.Index(unix, "func init() {")
	backendStart := strings.Index(unix, "func initClipboardBackend()")
	if initStart < 0 || backendStart < 0 || backendStart <= initStart {
		t.Fatal("expected package init wrapper before deferred backend initialization")
	}
	if strings.Contains(unix[initStart:backendStart], "exec.LookPath") {
		t.Fatal("package init still performs executable discovery")
	}

	for file, source := range map[string][]byte{
		"clipboard.go":      files["clipboard.go"],
		"clipboard_unix.go": files["clipboard_unix.go"],
	} {
		if _, err := format.Source(source); err != nil {
			t.Errorf("%s output is invalid Go: %v", file, err)
		}
	}
}
