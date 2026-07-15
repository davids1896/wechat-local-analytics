package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestASRRuntimeConfigPersistsSetupChoices(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := asrRuntimeConfig{
		Venv:        filepath.Join(t.TempDir(), "venv"),
		Model:       "small",
		Language:    "auto",
		Device:      "cpu",
		ComputeType: "int8",
	}
	if err := saveASRRuntimeConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadASRRuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("runtime config = %#v, want %#v", got, want)
	}
	venv, err := defaultASRVenvDir()
	if err != nil || venv != want.Venv {
		t.Fatalf("default venv = %q, %v; want %q", venv, err, want.Venv)
	}
	path, err := asrRuntimeConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not expose POSIX owner/group permission bits through
	// FileMode; the file remains protected by the user's inherited ACL.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime config mode = %o, want 600", info.Mode().Perm())
	}
}

func TestUnavailableVoiceTranscriptCacheIsRetried(t *testing.T) {
	for _, status := range []string{"unavailable", "error", ""} {
		if voiceTranscriptCacheUsable(map[string]any{"cache_version": voiceTranscriptCacheVersion, "status": status}) {
			t.Fatalf("status %q should not be reusable", status)
		}
	}
	for _, status := range []string{"ok", "no_speech"} {
		if !voiceTranscriptCacheUsable(map[string]any{"cache_version": voiceTranscriptCacheVersion, "status": status}) {
			t.Fatalf("status %q should be reusable", status)
		}
	}
}
