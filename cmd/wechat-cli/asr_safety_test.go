package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateASRForceVenvRejectsProtectedPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{string(os.PathSeparator), home, filepath.Dir(home)} {
		if err := validateASRForceVenv(path); err == nil {
			t.Fatalf("validateASRForceVenv(%q) succeeded", path)
		}
	}
}

func TestValidateASRForceVenvRequiresMarkerForCustomExistingDir(t *testing.T) {
	dir := t.TempDir()
	if err := validateASRForceVenv(dir); err == nil {
		t.Fatal("custom existing directory without marker was accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateASRForceVenv(dir); err != nil {
		t.Fatalf("marked venv rejected: %v", err)
	}
}

func TestValidateASRForceVenvRejectsWholeStateDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stateDir, err := appStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateASRForceVenv(stateDir); err == nil {
		t.Fatalf("whole state directory %q was accepted for recursive deletion", stateDir)
	}
}

func TestASRSetupDryRunDisclosesAndValidatesForceDelete(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "future-venv")
	result, err := asrSetup(asrSetupOptions{
		DryRun:            true,
		Force:             true,
		SkipModelDownload: true,
		Python:            "/usr/bin/python3",
		Venv:              dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	actions, _ := result["actions"].([]string)
	if len(actions) == 0 || actions[0] != "remove existing managed ASR virtualenv at "+dir {
		t.Fatalf("actions = %#v", actions)
	}
}
