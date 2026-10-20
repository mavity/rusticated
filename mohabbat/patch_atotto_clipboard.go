package mohabbat

import (
	"fmt"
	"strings"
)

func modulePathFromGomod(content string) string {
	for _, raw := range strings.Split(content, "\n") {
		fields := strings.Fields(strings.TrimSpace(raw))
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], `"`)
		}
	}
	return ""
}

func isKabibiClipboardDependency(module, gomodContent string) bool {
	return module == "github.com/atotto/clipboard" &&
		modulePathFromGomod(gomodContent) == "github.com/mavity/rusticated/kabibi"
}

func patchAtottoClipboardInMemory(files map[string][]byte) error {
	common, ok := files["clipboard.go"]
	if !ok {
		return fmt.Errorf("clipboard.go not found")
	}
	unix, ok := files["clipboard_unix.go"]
	if !ok {
		return fmt.Errorf("clipboard_unix.go not found")
	}

	commonSource, err := replaceExactOnce(
		"clipboard.go",
		string(common),
		`// Unsupported might be set true during clipboard init, to help callers decide
// whether or not to offer clipboard options.
var Unsupported bool`,
		`// IsUnsupported reports whether no system clipboard utility is available.
// Backend discovery is deferred until this function or a clipboard operation is called.
func IsUnsupported() bool {
	initializeClipboard()
	return unsupported
}

var (
	unsupported          bool
	initializeClipboard = func() {}
)`,
	)
	if err != nil {
		return err
	}

	unixSource, err := replaceExactOnce(
		"clipboard_unix.go",
		string(unix),
		"\t\"os/exec\"\n",
		"\t\"os/exec\"\n\t\"sync\"\n",
	)
	if err != nil {
		return err
	}

	unixSource, err = replaceExactOnce(
		"clipboard_unix.go",
		unixSource,
		"func init() {\n",
		`var clipboardInitOnce sync.Once

func init() {
	initializeClipboard = func() {
		clipboardInitOnce.Do(initClipboardBackend)
	}
}

func initClipboardBackend() {
`,
	)
	if err != nil {
		return err
	}

	unixSource, err = replaceExactOnce(
		"clipboard_unix.go",
		unixSource,
		"Unsupported = true",
		"unsupported = true",
	)
	if err != nil {
		return err
	}

	if strings.Count(unixSource, "if Unsupported {") != 2 {
		return fmt.Errorf("clipboard_unix.go: expected two Unsupported checks")
	}
	unixSource = strings.ReplaceAll(unixSource, "if Unsupported {", "if unsupported {")

	unixSource, err = replaceExactOnce(
		"clipboard_unix.go",
		unixSource,
		"func readAll() (string, error) {\n\tif unsupported {",
		"func readAll() (string, error) {\n\tinitializeClipboard()\n\tif unsupported {",
	)
	if err != nil {
		return err
	}
	unixSource, err = replaceExactOnce(
		"clipboard_unix.go",
		unixSource,
		"func writeAll(text string) error {\n\tif unsupported {",
		"func writeAll(text string) error {\n\tinitializeClipboard()\n\tif unsupported {",
	)
	if err != nil {
		return err
	}

	files["clipboard.go"] = []byte(commonSource)
	files["clipboard_unix.go"] = []byte(unixSource)
	return nil
}

func replaceExactOnce(file, source, old, replacement string) (string, error) {
	if strings.Count(source, old) != 1 {
		return "", fmt.Errorf("%s: expected one occurrence of patch anchor, found %d", file, strings.Count(source, old))
	}
	return strings.Replace(source, old, replacement, 1), nil
}
