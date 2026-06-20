package mohabbat

import (
	"fmt"
	"os"

	regast "mohabbat/mohabbat/regast"
)

// regastPatch defines a search pattern and its replacement.
type regastPatch struct {
	pat  string
	repl string
}

// applyRegastPatches applies a sequence of regast (node-aware regexp)
// transformations to a file. The engine owns the whole lifecycle of the source
// string — parsing the AST, building the structural map, and matching — so the
// caller just supplies patterns and replacements.
func applyRegastPatches(path string, patches []regastPatch) error {
	if len(patches) == 0 {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	content := string(data)
	for _, p := range patches {
		re, err := regast.Compile(p.pat)
		if err != nil {
			return fmt.Errorf("regast compile %q: %w", p.pat, err)
		}
		content = re.ReplaceAllString(content, p.repl)
	}

	return writeFileIfChanged(path, []byte(content))
}
