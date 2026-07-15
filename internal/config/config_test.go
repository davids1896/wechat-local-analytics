package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestPathUsesExplicitConfig(t *testing.T) {
	want := filepath.Join(t.TempDir(), "wxcli", "config.json")
	t.Setenv("WX_MCP_CONFIG", want)
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("Path = %q, want %q", got, filepath.Clean(want))
	}
}

func TestLoadDoesNotCreateDefaultConfigDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WECHAT_CLI_CONFIG", "")
	t.Setenv("WX_MCP_CONFIG", "")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".config")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only config load created state: %v", err)
	}
}

func TestLoadAppliesDBRootOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	root := filepath.Join(dir, "wxid_test_1234")
	if err := os.MkdirAll(filepath.Join(root, "db_storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"wxid":"old","db_root":"old","keys":{"salt":"key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WX_MCP_CONFIG", cfgPath)
	t.Setenv("WX_MCP_DB_ROOT", root)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBRoot != filepath.Clean(root) {
		t.Fatalf("DBRoot = %q, want %q", cfg.DBRoot, filepath.Clean(root))
	}
	if cfg.Wxid != "old" {
		t.Fatalf("existing wxid should be preserved, got %q", cfg.Wxid)
	}
}

func TestSaveAtomicallyReplacesWithPrivateMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"wxid":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_CLI_CONFIG", path)
	want := &Config{SchemaVersion: 2, Wxid: "wxid_new", DBRoot: "/safe/root", Keys: map[string]string{"salt": "key"}}
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Wxid != want.Wxid || got.DBRoot != want.DBRoot || got.Keys["salt"] != "key" {
		t.Fatalf("saved config = %#v", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
		}
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".config.json.tmp-*")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary config files = %#v/%v", matches, err)
	}
}

func TestUpdateSerializesConcurrentMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("WECHAT_CLI_CONFIG", path)
	if err := Save(&Config{SchemaVersion: 2, Keys: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	const writers = 24
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Update(func(cfg *Config) error {
				if cfg.Keys == nil {
					cfg.Keys = map[string]string{}
				}
				cfg.Keys[string(rune('A'+i))] = "value"
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Update: %v", err)
		}
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Keys) != writers {
		t.Fatalf("concurrent keys = %d, want %d: %#v", len(cfg.Keys), writers, cfg.Keys)
	}
}

func TestUpdateRejectsLockSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv("WECHAT_CLI_CONFIG", path)
	target := filepath.Join(dir, "lock-target")
	if err := os.WriteFile(target, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ".config.json.lock")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := Update(func(cfg *Config) error {
		cfg.Wxid = "should-not-write"
		return nil
	}); err == nil {
		t.Fatal("Update accepted a symbolic-link lock file")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "sentinel" {
		t.Fatalf("lock symlink target changed: %q/%v", data, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("config unexpectedly written: %v", err)
	}
}

func TestUpdateMutationErrorLeavesConfigUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("WECHAT_CLI_CONFIG", path)
	if err := Save(&Config{Wxid: "before"}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("stop")
	if err := Update(func(cfg *Config) error {
		cfg.Wxid = "after"
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("Update error = %v, want %v", err, wantErr)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Wxid != "before" {
		t.Fatalf("config changed after failed mutation: %#v", cfg)
	}
}

func TestSaveRejectsConfigSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Setenv("WECHAT_CLI_CONFIG", link)
	if err := Save(&Config{Wxid: "should-not-write"}); err == nil {
		t.Fatalf("Save accepted a config symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "sentinel" {
		t.Fatalf("symlink target changed: %q/%v", data, err)
	}
}

func TestSaveRejectsSymlinkParent(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(base, "linked")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t.Setenv("WECHAT_CLI_CONFIG", filepath.Join(linkDir, "config.json"))
	if err := Save(&Config{Wxid: "should-not-write"}); err == nil {
		t.Fatalf("Save accepted a symlink config parent")
	}
	if _, err := os.Stat(filepath.Join(realDir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("config unexpectedly written through symlink parent: %v", err)
	}
}

func TestSaveRejectsIntermediateSymlinkUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserHomeDir reads USERPROFILE on Windows and HOME on Unix.
	t.Setenv("USERPROFILE", home)
	realConfig := filepath.Join(home, "real-config")
	if err := os.MkdirAll(realConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realConfig, filepath.Join(home, ".config")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	path := filepath.Join(home, ".config", "wxcli", "config.json")
	t.Setenv("WECHAT_CLI_CONFIG", path)
	if err := Save(&Config{Wxid: "should-not-write"}); err == nil {
		t.Fatalf("Save accepted an intermediate symlink under home")
	}
	if _, err := os.Stat(filepath.Join(realConfig, "wxcli")); !os.IsNotExist(err) {
		t.Fatalf("config directory unexpectedly created through intermediate symlink: %v", err)
	}
}

func TestWriteConfigToRootPinsOpenedParent(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "live")
	moved := filepath.Join(base, "moved")
	attacker := filepath.Join(base, "attacker")
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(attacker, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(live)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(live, moved); err != nil {
		t.Skipf("renaming an opened directory is unavailable: %v", err)
	}
	if err := os.Symlink(attacker, live); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	data := []byte("anchored\n")
	if err := writeConfigToRoot(root, "config.json", data); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(moved, "config.json"))
	if err != nil || string(got) != string(data) {
		t.Fatalf("anchored config = %q/%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(attacker, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("config escaped through replacement symlink: %v", err)
	}
}

func TestAutoDetectDBRootUsesEnvOverride(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wxid_env_9999")
	if err := os.MkdirAll(filepath.Join(root, "db_storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WX_MCP_DB_ROOT", root)
	gotRoot, wxid, err := AutoDetectDBRoot()
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != filepath.Clean(root) {
		t.Fatalf("root = %q, want %q", gotRoot, filepath.Clean(root))
	}
	if wxid != "wxid_env" {
		t.Fatalf("wxid = %q, want wxid_env", wxid)
	}
}

func TestWithXWeChatFilesBase(t *testing.T) {
	root := filepath.Join("Users", "v", "Documents", "WeChat Files")
	got := withXWeChatFilesBase(root)
	want := []string{root, filepath.Join(root, "xwechat_files")}
	if len(got) != len(want) {
		t.Fatalf("variants = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("variants[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
