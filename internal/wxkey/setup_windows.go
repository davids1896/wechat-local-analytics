//go:build windows

package wxkey

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/r266-tech/wechat-cli/internal/config"
	"github.com/r266-tech/wechat-cli/internal/wcdb"
)

const (
	processVMRead           = 0x0010
	processQueryInformation = 0x0400

	memCommit    = 0x1000
	pageNoAccess = 0x01
	pageGuard    = 0x100

	th32csSnapProcess = 0x00000002
)

var errWindowsKeyScanDeadline = errors.New("windows key scan deadline exceeded")

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess         = kernel32.NewProc("OpenProcess")
	procCloseHandle         = kernel32.NewProc("CloseHandle")
	procVirtualQueryEx      = kernel32.NewProc("VirtualQueryEx")
	procReadProcessMemory   = kernel32.NewProc("ReadProcessMemory")
	procCreateToolhelp32    = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW     = kernel32.NewProc("Process32FirstW")
	procProcess32NextW      = kernel32.NewProc("Process32NextW")
	procGetCurrentProcessID = kernel32.NewProc("GetCurrentProcessId")
)

type windowsMemoryBasicInformation struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	_                 uint32
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
	_                 uint32
}

type windowsProcessEntry32 struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	CntThreads      uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

type windowsSourceDB struct {
	rel  string
	path string
	salt string
}

type windowsProcess struct {
	pid uint32
	exe string
}

type windowsSetupStats struct {
	SourceDBs            int      `json:"source_dbs"`
	TargetSalts          int      `json:"target_salts"`
	ScannedProcesses     int      `json:"scanned_processes"`
	ScannedPIDs          []uint32 `json:"scanned_pids,omitempty"`
	OpenProcessFailures  int      `json:"open_process_failures"`
	QueriedRegions       int      `json:"queried_regions"`
	ReadableRegions      int      `json:"readable_regions"`
	ReadAttempts         int      `json:"read_attempts"`
	ReadSuccesses        int      `json:"read_successes"`
	ReadBytes            uint64   `json:"read_bytes"`
	RawKeyLiterals       int      `json:"raw_key_literals"`
	TargetSaltLiterals   int      `json:"target_salt_literals"`
	UTF16RawKeyLiterals  int      `json:"utf16_raw_key_literals"`
	UTF16TargetLiterals  int      `json:"utf16_target_salt_literals"`
	BinarySaltHits       int      `json:"binary_salt_hits"`
	BinaryCandidates     int      `json:"binary_candidates"`
	MatchedSalts         int      `json:"matched_salts"`
	VerificationAttempts int      `json:"verification_attempts"`
	VerificationFailures int      `json:"verification_failures"`
	VerifiedDBs          int      `json:"verified_dbs"`
}

func runSetup() (*SetupResult, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	if cfg.DBRoot == "" {
		root, wxid, err := config.AutoDetectDBRoot()
		if err != nil {
			return nil, "", fmt.Errorf("detect Windows WeChat DB root: %w", err)
		}
		cfg.DBRoot = root
		if cfg.Wxid == "" {
			cfg.Wxid = wxid
		}
	}
	if cfg.Wxid == "" {
		cfg.Wxid = wxidFromAccountDir(cfg.DBRoot)
	}
	if cfg.Keys == nil {
		cfg.Keys = map[string]string{}
	}

	dbs, salts, err := windowsListSourceDBs(cfg.DBRoot)
	if err != nil {
		return nil, "", err
	}
	if len(dbs) == 0 {
		return nil, "", fmt.Errorf("no .db files found under %s", filepath.Join(cfg.DBRoot, "db_storage"))
	}

	lib, err := windowsFindWCDB()
	if err != nil {
		return nil, "", err
	}
	if err := wcdb.Bootstrap(lib); err != nil {
		return nil, "", err
	}

	procs, err := windowsTargetProcesses()
	if err != nil {
		return nil, "", err
	}
	if len(procs) == 0 {
		return nil, "", fmt.Errorf("no running Weixin.exe/WeChat.exe process found; log in to Windows WeChat first or set WECHAT_CLI_WECHAT_PID")
	}

	found := map[string]string{}
	binaryCandidates := map[string][]string{}
	stats := windowsSetupStats{SourceDBs: len(dbs), TargetSalts: len(salts)}
	var firstHitPID uint32
	scanTimeout := windowsKeyScanTimeout()
	scanDeadline := time.Time{}
	if scanTimeout > 0 {
		scanDeadline = time.Now().Add(scanTimeout)
	}
	timedOut := false
	for _, p := range procs {
		if len(found) == len(salts) {
			break
		}
		if windowsKeyScanDeadlineExceeded(scanDeadline) {
			timedOut = true
			break
		}
		stats.ScannedProcesses++
		stats.ScannedPIDs = append(stats.ScannedPIDs, p.pid)
		before := windowsCandidateCount(found, binaryCandidates)
		if err := windowsScanProcess(p.pid, salts, found, binaryCandidates, scanDeadline, &stats); err != nil {
			if firstHitPID == 0 && windowsCandidateCount(found, binaryCandidates) > before {
				firstHitPID = p.pid
			}
			if errors.Is(err, errWindowsKeyScanDeadline) {
				timedOut = true
				break
			}
			continue
		}
		if firstHitPID == 0 && windowsCandidateCount(found, binaryCandidates) > before {
			firstHitPID = p.pid
		}
	}

	var results []ResultEntry
	verified := map[string]string{}
	for _, db := range dbs {
		candidates := windowsCandidateKeys(found[db.salt], binaryCandidates[db.salt])
		if len(candidates) == 0 {
			continue
		}
		var key string
		for _, candidate := range candidates {
			stats.VerificationAttempts++
			if windowsVerifyDBKey(db.path, candidate, db.salt) {
				key = candidate
				break
			}
			stats.VerificationFailures++
		}
		if key == "" {
			continue
		}
		verified[db.salt] = key
		results = append(results, ResultEntry{
			DBRel:    filepath.ToSlash(db.rel),
			DBPath:   db.path,
			SaltHex:  db.salt,
			KeyHex:   key,
			VerifyAs: "windows-process-raw-key",
		})
	}
	stats.MatchedSalts = windowsMatchedSaltCount(found, binaryCandidates)
	if len(verified) == 0 {
		diagnostics := windowsFailureDiagnostics(stats)
		if timedOut {
			return nil, "", fmt.Errorf("Windows key scan timed out after %s before finding usable keys; diagnostics=%s", scanTimeout.Round(time.Second), diagnostics)
		}
		return nil, "", fmt.Errorf("no usable Windows WeChat raw keys found after scanning %d process(es); diagnostics=%s", stats.ScannedProcesses, diagnostics)
	}

	keyEpoch := time.Now().Unix()
	if err := config.Update(func(current *config.Config) error {
		if current.DBRoot != "" && !strings.EqualFold(filepath.Clean(current.DBRoot), filepath.Clean(cfg.DBRoot)) {
			return fmt.Errorf("WeChat account changed during Windows key scan: db_root %q -> %q; retry", cfg.DBRoot, current.DBRoot)
		}
		if current.Wxid != "" && cfg.Wxid != "" && current.Wxid != cfg.Wxid {
			return fmt.Errorf("WeChat account changed during Windows key scan: wxid mismatch; retry")
		}
		if current.DBRoot == "" {
			current.DBRoot = cfg.DBRoot
		}
		if current.Wxid == "" {
			current.Wxid = cfg.Wxid
		}
		if current.Keys == nil {
			current.Keys = map[string]string{}
		}
		for salt, key := range verified {
			current.Keys[salt] = key
		}
		if current.SchemaVersion < 2 {
			current.SchemaVersion = 2
		}
		if keyEpoch >= current.KeyEpoch {
			current.KeyPID = int(firstHitPID)
			current.KeyEpoch = keyEpoch
		}
		return nil
	}); err != nil {
		return nil, "", err
	}
	cfg, err = config.Load()
	if err != nil {
		return nil, "", fmt.Errorf("reload Windows key config: %w", err)
	}
	stats.VerifiedDBs = len(results)
	statsJSON, _ := json.Marshal(stats)
	cfgPath, _ := config.Path()
	res := &SetupResult{
		PID:        int(firstHitPID),
		Root:       cfg.DBRoot,
		WxID:       cfg.Wxid,
		ConfigPath: cfgPath,
		Stats:      statsJSON,
		Results:    results,
		Keys:       verified,
	}
	msg := fmt.Sprintf("Windows key scan OK: verified %d/%d db files from %d process(es)\n", stats.VerifiedDBs, stats.SourceDBs, stats.ScannedProcesses)
	if timedOut {
		msg = fmt.Sprintf("Windows key scan OK with partial coverage before timeout %s: verified %d/%d db files from %d process(es)\n", scanTimeout.Round(time.Second), stats.VerifiedDBs, stats.SourceDBs, stats.ScannedProcesses)
	}
	return res, msg, nil
}

func windowsFailureDiagnostics(stats windowsSetupStats) string {
	stats.ScannedPIDs = nil
	b, err := json.Marshal(stats)
	if err != nil {
		return `{"error":"encode diagnostics"}`
	}
	return string(b)
}

func windowsCandidateCount(literals map[string]string, binary map[string][]string) int {
	n := len(literals)
	for _, candidates := range binary {
		n += len(candidates)
	}
	return n
}

func windowsMatchedSaltCount(literals map[string]string, binary map[string][]string) int {
	matched := make(map[string]bool, len(literals)+len(binary))
	for salt := range literals {
		matched[salt] = true
	}
	for salt, candidates := range binary {
		if len(candidates) > 0 {
			matched[salt] = true
		}
	}
	return len(matched)
}

func windowsCandidateKeys(literal string, binary []string) []string {
	seen := map[string]bool{}
	var out []string
	if literal != "" {
		seen[literal] = true
		out = append(out, literal)
	}
	for _, candidate := range binary {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	return out
}

func windowsKeyScanTimeout() time.Duration {
	raw := firstEnv("WECHAT_CLI_KEY_SCAN_TIMEOUT", "WECHAT_CLI_WINDOWS_KEY_SCAN_TIMEOUT", "WX_MCP_KEY_SCAN_TIMEOUT")
	if raw == "" {
		return 3 * time.Minute
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d < 0 {
			return 0
		}
		return d
	}
	if sec, err := strconv.Atoi(raw); err == nil {
		if sec < 0 {
			return 0
		}
		return time.Duration(sec) * time.Second
	}
	return 3 * time.Minute
}

func windowsKeyScanDeadlineExceeded(deadline time.Time) bool {
	return !deadline.IsZero() && time.Now().After(deadline)
}

func windowsFindWCDB() (string, error) {
	names := []string{"libWCDB.dll", "WCDB.dll", "e_sqlcipher.dll"}
	var candidates []string
	for _, env := range []string{"WECHAT_CLI_WCDB_LIB", "WECHAT_CLI_WCDB_DYLIB", "WX_MCP_WCDB_LIB", "WX_MCP_WCDB_DYLIB"} {
		if p := strings.TrimSpace(os.Getenv(env)); p != "" {
			candidates = append(candidates, p)
		}
	}
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			dir := filepath.Dir(exe)
			for _, name := range names {
				candidates = append(candidates, filepath.Join(dir, name), filepath.Join(dir, "lib", name), filepath.Join(dir, "..", "lib", name))
			}
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(cwd, name), filepath.Join(cwd, "lib", name))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(home, ".config", "wxcli", "lib", name))
		}
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("WCDB/SQLCipher DLL not found for Windows key verification")
}

func windowsListSourceDBs(root string) ([]windowsSourceDB, map[string]bool, error) {
	base := filepath.Join(root, "db_storage")
	var out []windowsSourceDB
	salts := map[string]bool{}
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if filepath.Ext(name) != ".db" || strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
			return nil
		}
		salt, err := windowsReadSaltHex(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		out = append(out, windowsSourceDB{rel: rel, path: path, salt: salt})
		salts[salt] = true
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, salts, err
}

func windowsReadSaltHex(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b := make([]byte, 16)
	if _, err := f.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func windowsVerifyDBKey(path, keyHex, saltHex string) bool {
	db, err := wcdb.OpenWithEncKey(path, keyHex, saltHex)
	if err != nil {
		return false
	}
	defer db.Close()
	rows, err := db.Query("SELECT count(*) AS c FROM sqlite_master")
	return err == nil && len(rows) > 0
}

func windowsTargetProcesses() ([]windowsProcess, error) {
	if raw := firstEnv("WECHAT_CLI_WECHAT_PID", "WX_MCP_WECHAT_PID"); raw != "" {
		var out []windowsProcess
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			if part == "" {
				continue
			}
			pid, err := strconv.ParseUint(part, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("parse WECHAT_CLI_WECHAT_PID=%q: %w", raw, err)
			}
			out = append(out, windowsProcess{pid: uint32(pid), exe: "env"})
		}
		return out, nil
	}
	names := map[string]bool{"weixin.exe": true, "wechat.exe": true}
	if raw := firstEnv("WECHAT_CLI_WECHAT_PROCESS", "WX_MCP_WECHAT_PROCESS"); raw != "" {
		names = map[string]bool{}
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			part = strings.ToLower(strings.TrimSpace(part))
			if part == "" {
				continue
			}
			if !strings.HasSuffix(part, ".exe") {
				part += ".exe"
			}
			names[part] = true
		}
	}
	all, err := windowsEnumerateProcesses()
	if err != nil {
		return nil, err
	}
	var out []windowsProcess
	currentPID := windowsCurrentPID()
	for _, p := range all {
		if p.pid == currentPID {
			continue
		}
		if names[strings.ToLower(p.exe)] {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].exe != out[j].exe {
			return out[i].exe < out[j].exe
		}
		return out[i].pid < out[j].pid
	})
	return out, nil
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

func windowsEnumerateProcesses() ([]windowsProcess, error) {
	snap, _, err := procCreateToolhelp32.Call(th32csSnapProcess, 0)
	if snap == uintptr(syscall.InvalidHandle) || snap == 0 {
		return nil, err
	}
	defer procCloseHandle.Call(snap)
	var entry windowsProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	r, _, err := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&entry)))
	if r == 0 {
		return nil, err
	}
	var out []windowsProcess
	for {
		out = append(out, windowsProcess{
			pid: entry.ProcessID,
			exe: syscall.UTF16ToString(entry.ExeFile[:]),
		})
		entry.Size = uint32(unsafe.Sizeof(entry))
		r, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&entry)))
		if r == 0 {
			break
		}
	}
	return out, nil
}

func windowsCurrentPID() uint32 {
	r, _, _ := procGetCurrentProcessID.Call()
	return uint32(r)
}

func windowsScanProcess(pid uint32, targetSalts map[string]bool, found map[string]string, binaryCandidates map[string][]string, deadline time.Time, stats *windowsSetupStats) error {
	h, _, err := procOpenProcess.Call(processVMRead|processQueryInformation, 0, uintptr(pid))
	if h == 0 {
		stats.OpenProcessFailures++
		return err
	}
	defer procCloseHandle.Call(h)
	const maxUserAddress = uintptr(0x00007fffffffffff)
	for addr := uintptr(0); addr < maxUserAddress; {
		if len(found) == len(targetSalts) {
			return nil
		}
		if windowsKeyScanDeadlineExceeded(deadline) {
			return errWindowsKeyScanDeadline
		}
		var m windowsMemoryBasicInformation
		r, _, _ := procVirtualQueryEx.Call(h, addr, uintptr(unsafe.Pointer(&m)), unsafe.Sizeof(m))
		if r == 0 {
			addr += 0x10000
			continue
		}
		stats.QueriedRegions++
		next := m.BaseAddress + m.RegionSize
		if next <= addr {
			return nil
		}
		if windowsReadableRegion(m) {
			stats.ReadableRegions++
			if err := windowsScanRegion(h, m.BaseAddress, m.RegionSize, targetSalts, found, binaryCandidates, deadline, stats); err != nil {
				return err
			}
		}
		addr = next
	}
	return nil
}

func windowsReadableRegion(m windowsMemoryBasicInformation) bool {
	return m.State == memCommit && m.RegionSize > 0 && m.Protect&pageNoAccess == 0 && m.Protect&pageGuard == 0
}

func windowsScanRegion(process uintptr, base, size uintptr, targetSalts map[string]bool, found map[string]string, binaryCandidates map[string][]string, deadline time.Time, stats *windowsSetupStats) error {
	const chunkSize = 4 << 20
	var overlap []byte
	for off := uintptr(0); off < size; {
		if len(found) == len(targetSalts) {
			return nil
		}
		if windowsKeyScanDeadlineExceeded(deadline) {
			return errWindowsKeyScanDeadline
		}
		n := chunkSize
		if remain := size - off; remain < uintptr(n) {
			n = int(remain)
		}
		buf := make([]byte, n)
		var got uintptr
		stats.ReadAttempts++
		r, _, _ := procReadProcessMemory.Call(process, base+off, uintptr(unsafe.Pointer(&buf[0])), uintptr(n), uintptr(unsafe.Pointer(&got)))
		if r != 0 && got > 0 {
			stats.ReadSuccesses++
			stats.ReadBytes += uint64(got)
			data := append(append([]byte{}, overlap...), buf[:got]...)
			scanRawKeyLiterals(data, targetSalts, found, stats)
			scanUTF16RawKeyLiterals(data, targetSalts, found, stats)
			scanBinaryRawKeyCandidates(data, targetSalts, binaryCandidates, stats)
			if len(data) > 256 {
				overlap = append(overlap[:0], data[len(data)-256:]...)
			} else {
				overlap = append(overlap[:0], data...)
			}
		}
		off += uintptr(n)
	}
	return nil
}

func scanRawKeyLiterals(data []byte, targetSalts map[string]bool, found map[string]string, stats *windowsSetupStats) int {
	var hits int
	for i := 0; i+99 <= len(data); i++ {
		if data[i] != 'x' || data[i+1] != '\'' || data[i+98] != '\'' {
			continue
		}
		hexBytes := data[i+2 : i+98]
		if !asciiHex(hexBytes) {
			continue
		}
		stats.RawKeyLiterals++
		salt := strings.ToLower(string(hexBytes[64:96]))
		if !targetSalts[salt] {
			continue
		}
		stats.TargetSaltLiterals++
		key := strings.ToLower(string(hexBytes[:64]))
		if _, err := hex.DecodeString(key); err != nil {
			continue
		}
		found[salt] = key
		hits++
	}
	return hits
}

func scanUTF16RawKeyLiterals(data []byte, targetSalts map[string]bool, found map[string]string, stats *windowsSetupStats) int {
	var hits int
	for i := 0; i+198 <= len(data); i++ {
		if data[i] != 'x' || data[i+1] != 0 || data[i+2] != '\'' || data[i+3] != 0 || data[i+196] != '\'' || data[i+197] != 0 {
			continue
		}
		hexBytes := make([]byte, 96)
		valid := true
		for j := range hexBytes {
			c := data[i+4+j*2]
			if data[i+5+j*2] != 0 || !asciiHexByte(c) {
				valid = false
				break
			}
			hexBytes[j] = c
		}
		if !valid {
			continue
		}
		stats.UTF16RawKeyLiterals++
		salt := strings.ToLower(string(hexBytes[64:96]))
		if !targetSalts[salt] {
			continue
		}
		stats.UTF16TargetLiterals++
		key := strings.ToLower(string(hexBytes[:64]))
		found[salt] = key
		hits++
	}
	return hits
}

const maxBinaryCandidatesPerSalt = 64

func scanBinaryRawKeyCandidates(data []byte, targetSalts map[string]bool, candidates map[string][]string, stats *windowsSetupStats) {
	for saltHex := range targetSalts {
		salt, err := hex.DecodeString(saltHex)
		if err != nil || len(salt) != 16 {
			continue
		}
		for searchFrom := 0; searchFrom < len(data); {
			rel := bytes.Index(data[searchFrom:], salt)
			if rel < 0 {
				break
			}
			idx := searchFrom + rel
			stats.BinarySaltHits++
			if idx >= 32 {
				addBinaryCandidate(saltHex, data[idx-32:idx], candidates, stats)
			}
			if idx+len(salt)+32 <= len(data) {
				addBinaryCandidate(saltHex, data[idx+len(salt):idx+len(salt)+32], candidates, stats)
			}
			searchFrom = idx + 1
		}
	}
}

func addBinaryCandidate(salt string, raw []byte, candidates map[string][]string, stats *windowsSetupStats) {
	if len(raw) != 32 || allZero(raw) || len(candidates[salt]) >= maxBinaryCandidatesPerSalt {
		return
	}
	key := hex.EncodeToString(raw)
	for _, existing := range candidates[salt] {
		if existing == key {
			return
		}
	}
	candidates[salt] = append(candidates[salt], key)
	stats.BinaryCandidates++
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func asciiHex(b []byte) bool {
	for _, c := range b {
		if !asciiHexByte(c) {
			return false
		}
	}
	return true
}

func asciiHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func wxidFromAccountDir(path string) string {
	name := filepath.Base(filepath.Clean(path))
	if idx := strings.LastIndex(name, "_"); idx > 0 {
		return name[:idx]
	}
	return name
}
