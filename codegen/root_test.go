package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModuleRootIsTheNearestDirectoryHoldingGoMod(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "app", "models")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(nested)

	got, err := ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}

	// The temp dir may sit behind a symlink (macOS /var → /private/var).
	want, _ := filepath.EvalSymlinks(root)
	if resolved, _ := filepath.EvalSymlinks(got); resolved != want {
		t.Fatalf("ModuleRoot() = %s, want %s", got, root)
	}
}
