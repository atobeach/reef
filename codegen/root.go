package codegen

import (
	"io/fs"
	"os"
	"path/filepath"
)

// ModuleRoot is the directory holding go.mod, found from the working
// directory up: where generated files are written (`go generate` runs from
// the generating package's directory) and where tests find module files.
func ModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fs.ErrNotExist
		}
		dir = parent
	}
}
