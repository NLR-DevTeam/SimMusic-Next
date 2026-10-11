package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEditableBuiltinDeployment(t *testing.T) {
	root := t.TempDir()
	dir, err := DeployLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "main.js")
	if err := os.WriteFile(path, []byte("// user edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DeployLocal(root); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "// user edit" {
		t.Fatal("deployment overwrote user edits")
	}
	if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err != nil {
		t.Fatal(err)
	}
}
