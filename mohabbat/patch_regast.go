package mohabbat

import (
	"fmt"
	"os"

	"mohabbat/mohabbat/regast"
)

// regastPatch defines a search pattern and its replacement.
type regastPatch struct {
	pat  string
	repl string
}

// applyRegastPatches applies a sequence of regast transformations to a file.
func applyRegastPatches(path string, patches []regastPatch) error {
	if len(patches) == 0 {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	src, err := regast.Preprocess(path, data)
	if err != nil {
		return fmt.Errorf("regast preprocess %s: %w", path, err)
	}

	content := data
	for _, p := range patches {
		pattern, err := regast.Compile(p.pat)
		if err != nil {
			return fmt.Errorf("regast compile %q: %w", p.pat, err)
		}
		
		updated, err := pattern.Replace(src, p.repl)
		if err != nil {
			return fmt.Errorf("regast replace %s with %q: %w", path, p.pat, err)
		}
		
		if string(updated) != string(content) {
			content = updated
			// Re-preprocess if content changed to keep node spans in sync for subsequent patches.
			// This is slightly inefficient but ensures correctness for overlapping/sequential patches.
			src, err = regast.Preprocess(path, content)
			if err != nil {
				return fmt.Errorf("regast re-preprocess %s: %w", path, err)
			}
		}
	}

	return writeFileIfChanged(path, content)
}
