//go:build windows

package wxkey

import (
	"encoding/hex"
	"testing"
	"time"
)

func TestScanRawKeyLiteralsFindsTargetSalt(t *testing.T) {
	key := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	salt := "ffeeddccbbaa99887766554433221100"
	data := []byte("noise x'" + key + salt + "' tail")
	found := map[string]string{}
	stats := &windowsSetupStats{}
	hits := scanRawKeyLiterals(data, map[string]bool{salt: true}, found, stats)
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
	if got := found[salt]; got != key {
		t.Fatalf("found key = %q, want %q", got, key)
	}
	if stats.RawKeyLiterals != 1 || stats.TargetSaltLiterals != 1 {
		t.Fatalf("literal stats = raw:%d target:%d, want 1/1", stats.RawKeyLiterals, stats.TargetSaltLiterals)
	}
}

func TestScanRawKeyLiteralsIgnoresNonTargetSalt(t *testing.T) {
	key := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	salt := "ffeeddccbbaa99887766554433221100"
	data := []byte("x'" + key + salt + "'")
	found := map[string]string{}
	stats := &windowsSetupStats{}
	hits := scanRawKeyLiterals(data, map[string]bool{"00000000000000000000000000000000": true}, found, stats)
	if hits != 0 {
		t.Fatalf("hits = %d, want 0", hits)
	}
	if len(found) != 0 {
		t.Fatalf("found = %v, want empty", found)
	}
	if stats.RawKeyLiterals != 1 || stats.TargetSaltLiterals != 0 {
		t.Fatalf("literal stats = raw:%d target:%d, want 1/0", stats.RawKeyLiterals, stats.TargetSaltLiterals)
	}
}

func TestScanUTF16RawKeyLiteralsFindsTargetSalt(t *testing.T) {
	key := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	salt := "ffeeddccbbaa99887766554433221100"
	literal := "x'" + key + salt + "'"
	data := []byte("noise ")
	for i := range len(literal) {
		data = append(data, literal[i], 0)
	}
	data = append(data, []byte(" tail")...)
	found := map[string]string{}
	stats := &windowsSetupStats{}
	hits := scanUTF16RawKeyLiterals(data, map[string]bool{salt: true}, found, stats)
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
	if got := found[salt]; got != key {
		t.Fatalf("found key = %q, want %q", got, key)
	}
	if stats.UTF16RawKeyLiterals != 1 || stats.UTF16TargetLiterals != 1 {
		t.Fatalf("UTF-16 literal stats = raw:%d target:%d, want 1/1", stats.UTF16RawKeyLiterals, stats.UTF16TargetLiterals)
	}
}

func TestScanBinaryRawKeyCandidatesFindsAdjacentKey(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	saltHex := "ffeeddccbbaa99887766554433221100"
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		t.Fatal(err)
	}
	data := append([]byte("noise"), key...)
	data = append(data, salt...)
	data = append(data, []byte("tail")...)
	candidates := map[string][]string{}
	stats := &windowsSetupStats{}
	scanBinaryRawKeyCandidates(data, map[string]bool{saltHex: true}, candidates, stats)
	if got := candidates[saltHex]; len(got) != 1 || got[0] != hex.EncodeToString(key) {
		t.Fatalf("candidates = %v, want adjacent key", got)
	}
	if stats.BinarySaltHits != 1 || stats.BinaryCandidates != 1 {
		t.Fatalf("binary stats = hits:%d candidates:%d, want 1/1", stats.BinarySaltHits, stats.BinaryCandidates)
	}
}

func TestWindowsKeyScanTimeoutEnv(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "duration", value: "1500ms", want: 1500 * time.Millisecond},
		{name: "seconds", value: "7", want: 7 * time.Second},
		{name: "disabled", value: "-1", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WECHAT_CLI_KEY_SCAN_TIMEOUT", tt.value)
			if got := windowsKeyScanTimeout(); got != tt.want {
				t.Fatalf("windowsKeyScanTimeout = %s, want %s", got, tt.want)
			}
		})
	}
}
