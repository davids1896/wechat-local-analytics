package wxkey

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExecutableSearchDirsIncludesResolvedSymlinkDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not consistently available on Windows CI")
	}
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "share", "wechat-cli")
	shimDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realExe := filepath.Join(realDir, "wechat-cli")
	if err := os.WriteFile(realExe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(shimDir, "wechat-cli")
	if err := os.Symlink(realExe, shim); err != nil {
		t.Fatal(err)
	}

	got := executableSearchDirs(shim)
	resolvedRealDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{shimDir, resolvedRealDir}
	if len(got) != len(want) {
		t.Fatalf("dirs = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dirs = %#v, want %#v", got, want)
		}
	}
}
