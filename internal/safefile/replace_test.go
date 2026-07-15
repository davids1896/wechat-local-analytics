package safefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceKeepsOldDestinationUntilPublish(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "state")
	src := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Replace(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("destination = %q, want new", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
}
