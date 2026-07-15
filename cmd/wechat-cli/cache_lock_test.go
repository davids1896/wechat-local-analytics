package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInheritedCacheLockRejectsArbitraryPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_CLI_CACHE_LOCK_HELD", victim)

	unlock, acquired, _, err := acquireCacheRefreshLock()
	if err == nil || acquired || unlock != nil {
		t.Fatalf("arbitrary inherited lock accepted: acquired=%v unlock=%v err=%v", acquired, unlock != nil, err)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatalf("victim changed: %v", statErr)
	}
}

func TestInheritedCacheLockAcceptsManagedPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WECHAT_CLI_CACHE_LOCK_HELD", "")
	managed, err := expectedCacheRefreshLockDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_CLI_CACHE_LOCK_HELD", managed)

	unlock, acquired, got, err := acquireCacheRefreshLock()
	if err != nil || !acquired || unlock == nil || got != managed {
		t.Fatalf("managed lock rejected: acquired=%v got=%q err=%v", acquired, got, err)
	}
	unlock()
	if _, statErr := os.Stat(managed); !os.IsNotExist(statErr) {
		t.Fatalf("managed lock still exists: %v", statErr)
	}
}
