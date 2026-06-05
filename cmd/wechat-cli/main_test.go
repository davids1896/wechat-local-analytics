package main

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r266-tech/wechat-cli/internal/config"
	"github.com/r266-tech/wechat-cli/internal/wcdb"
	"github.com/r266-tech/wechat-cli/internal/wxkey"
	"github.com/r266-tech/wechat-cli/internal/wxparse"
)

func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	var buf bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		_, err := buf.ReadFrom(r)
		errCh <- err
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return buf.Bytes()
}

func TestParseTS_UnixSeconds(t *testing.T) {
	got, err := parseTS("1776330000")
	if err != nil || got != 1776330000 {
		t.Errorf("parseTS('1776330000') = (%d,%v), want (1776330000,nil)", got, err)
	}
}

func TestParseTS_DateOnly(t *testing.T) {
	got, err := parseTS("2026-04-16")
	if err != nil {
		t.Fatalf("parseTS error: %v", err)
	}
	want := time.Date(2026, 4, 16, 0, 0, 0, 0, time.Local).Unix()
	if got != want {
		t.Errorf("parseTS('2026-04-16') = %d, want %d", got, want)
	}
}

func TestParseTS_DateTime(t *testing.T) {
	got, err := parseTS("2026-04-16T12:30:45")
	if err != nil {
		t.Fatalf("parseTS error: %v", err)
	}
	want := time.Date(2026, 4, 16, 12, 30, 45, 0, time.Local).Unix()
	if got != want {
		t.Errorf("parseTS = %d, want %d", got, want)
	}
}

func TestParseTS_DateTimeWithSpace(t *testing.T) {
	got, err := parseTS("2026-04-16 12:30:45")
	if err != nil {
		t.Fatalf("parseTS error: %v", err)
	}
	want := time.Date(2026, 4, 16, 12, 30, 45, 0, time.Local).Unix()
	if got != want {
		t.Errorf("parseTS = %d, want %d", got, want)
	}
}

func TestParseTS_Empty(t *testing.T) {
	got, err := parseTS("")
	if err != nil || got != 0 {
		t.Errorf("parseTS('') = (%d,%v), want (0,nil)", got, err)
	}
}

func TestParseTS_Bad(t *testing.T) {
	_, err := parseTS("not-a-time")
	if err == nil {
		t.Error("parseTS('not-a-time') should error")
	}
}

func TestTalkerHash(t *testing.T) {
	// md5("wxid_testtalker0001") known value (verified via python hashlib).
	got := talkerHash("wxid_testtalker0001")
	want := "b2ed09282c82cadc5646d5a6c462c429"
	if got != want {
		t.Errorf("talkerHash = %q, want %q", got, want)
	}
}

func TestSenderPrefixRe(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"wxid_puf:\nhello", "hello"},
		{"abc-123:\r\nworld", "world"},
		{"  wxid_x:\nbody", "body"}, // leading whitespace
		{"plain text no prefix", "plain text no prefix"},
		{"https://example.com\nstuff", "https://example.com\nstuff"}, // URL not stripped (':' followed by '/')
		{"wxid_x: still text", "wxid_x: still text"},                 // ':' not followed by newline
	}
	for _, c := range cases {
		got := senderPrefixRe.ReplaceAllString(c.in, "")
		if got != c.want {
			t.Errorf("strip(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCLIHelpIsDefault(t *testing.T) {
	if !maybeRunCLI(nil) {
		t.Fatal("empty args should be handled by CLI help")
	}
	if !maybeRunCLI([]string{"help"}) {
		t.Fatal("help should be handled by CLI")
	}
}

func TestParseGlobalCLIOptions(t *testing.T) {
	opts, args, err := parseGlobalCLIOptions([]string{"--json", "--pretty", "resolve-chat", "张三", "--compact=false"})
	if err != nil {
		t.Fatalf("parseGlobalCLIOptions error: %v", err)
	}
	if !opts.Pretty {
		t.Fatalf("Pretty = false, want true")
	}
	if strings.Join(args, "\x00") != strings.Join([]string{"resolve-chat", "张三"}, "\x00") {
		t.Fatalf("args = %#v", args)
	}
}

func TestParseGlobalCLIOptionsCompactWins(t *testing.T) {
	opts, args, err := parseGlobalCLIOptions([]string{"--pretty", "--compact", "sessions"})
	if err != nil {
		t.Fatalf("parseGlobalCLIOptions error: %v", err)
	}
	if opts.Pretty {
		t.Fatalf("Pretty = true, want false")
	}
	if len(args) != 1 || args[0] != "sessions" {
		t.Fatalf("args = %#v", args)
	}
}

func TestParseGlobalCLIOptionsStrictReadOnly(t *testing.T) {
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "")
	opts, args, err := parseGlobalCLIOptions([]string{"--strict-read-only", "timeline", "张三"})
	if err != nil {
		t.Fatalf("parseGlobalCLIOptions error: %v", err)
	}
	if !opts.StrictReadOnly || !strictReadOnlyMode() {
		t.Fatalf("strict read-only not enabled: opts=%#v env=%q", opts, os.Getenv("WECHAT_CLI_STRICT_READ_ONLY"))
	}
	if strings.Join(args, "\x00") != strings.Join([]string{"timeline", "张三"}, "\x00") {
		t.Fatalf("args = %#v", args)
	}
	opts, args, err = parseGlobalCLIOptions([]string{"--strict-read-only=false", "sessions"})
	if err != nil {
		t.Fatalf("parseGlobalCLIOptions disable error: %v", err)
	}
	if opts.StrictReadOnly || strictReadOnlyMode() {
		t.Fatalf("strict read-only should be disabled: opts=%#v env=%q", opts, os.Getenv("WECHAT_CLI_STRICT_READ_ONLY"))
	}
	if len(args) != 1 || args[0] != "sessions" {
		t.Fatalf("args = %#v", args)
	}
}

func TestCLIVersionCommand(t *testing.T) {
	out := captureStdout(t, func() {
		if !maybeRunCLI([]string{"--version"}) {
			t.Fatal("--version was not handled by CLI")
		}
	})
	var env cliSuccessEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("version output is not success envelope: %v\n%s", err, string(out))
	}
	data, ok := env.Data.(map[string]any)
	if !ok || data["name"] != appName || data["version"] != appVersion {
		t.Fatalf("version data = %#v", env.Data)
	}
}

func TestCLIHelpDocumentForCommand(t *testing.T) {
	raw := cliHelpDocument("timeline")
	doc, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("help doc type = %T", raw)
	}
	cmd, ok := doc["command"].(cliCommandSpec)
	if !ok || cmd.Tool != "chat_timeline" {
		t.Fatalf("command spec = %#v", doc["command"])
	}
	td, ok := doc["tool"].(toolDef)
	if !ok || td.Name != "chat_timeline" {
		t.Fatalf("tool def = %#v", doc["tool"])
	}
}

func TestRootHelpCommandsExposeStableNameField(t *testing.T) {
	out := captureStdout(t, func() {
		runCLIHelp("", cliOptions{})
	})
	var env cliSuccessEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("help output is not success envelope: %v\n%s", err, string(out))
	}
	doc, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("help data = %#v", env.Data)
	}
	commands, ok := doc["commands"].([]any)
	if !ok || len(commands) == 0 {
		t.Fatalf("commands = %#v", doc["commands"])
	}
	seen := map[string]bool{}
	for _, raw := range commands {
		cmd, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("command entry = %#v", raw)
		}
		if cmd["name"] == nil || cmd["command"] == nil || cmd["name"] != cmd["command"] {
			t.Fatalf("command entry missing stable name/command: %#v", cmd)
		}
		seen[fmt.Sprint(cmd["name"])] = true
	}
	if seen["serve-mcp"] {
		t.Fatalf("help still exposes serve-mcp: %#v", seen)
	}
	if !seen["tail"] {
		t.Fatalf("help does not expose tail command: %#v", seen)
	}
}

func TestCLIHelpDocumentForUpdateCommand(t *testing.T) {
	raw := cliHelpDocument("update")
	doc, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("help doc type = %T", raw)
	}
	cmd, ok := doc["command"].(cliCommandSpec)
	if !ok || cmd.Command != "update" || cmd.Tool != "" {
		t.Fatalf("command spec = %#v", doc["command"])
	}
	if _, ok := doc["tool"]; ok {
		t.Fatalf("update help should not expose a tool schema: %#v", doc["tool"])
	}
}

func TestReadEventsCursorAndJSONLHelpers(t *testing.T) {
	args := map[string]any{"cursor": "local_id:42"}
	applyReadEventsCursor(args, getStr(args, "cursor"))
	if args["after_message"] != int64(42) {
		t.Fatalf("after_message = %#v, want 42", args["after_message"])
	}
	if cursor := readEventsCursorFromMessageID(map[string]any{"local_id": int64(43)}); cursor != "local_id:43" {
		t.Fatalf("cursor = %q, want local_id:43", cursor)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	env := map[string]any{
		"events": []map[string]any{
			{"type": "message", "cursor": "local_id:43"},
			{"type": "message", "cursor": "local_id:44"},
		},
	}
	if err := writeReadEventsJSONL(enc, env); err != nil {
		t.Fatalf("writeReadEventsJSONL error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"local_id:43"`) || !strings.Contains(lines[1], `"local_id:44"`) {
		t.Fatalf("jsonl output = %q", buf.String())
	}
}

func TestResolveLooseSenderSupportsMeAlias(t *testing.T) {
	srv := &server{cfg: &config.Config{Wxid: "wxid_3qqa0aja1kf612_f06d"}}
	got, err := srv.resolveLooseSenderArg(map[string]any{"sender": "me"})
	if err != nil {
		t.Fatalf("sender=me error: %v", err)
	}
	if got != "wxid_3qqa0aja1kf612" {
		t.Fatalf("sender=me resolved %q", got)
	}
	got, err = srv.resolveLooseSenderArg(map[string]any{"from_me": true})
	if err != nil {
		t.Fatalf("from_me error: %v", err)
	}
	if got != "wxid_3qqa0aja1kf612" {
		t.Fatalf("from_me resolved %q", got)
	}
}

func TestSearchRowsOffsetAndLimit(t *testing.T) {
	rows := []wcdb.Row{
		{"local_id": int64(1)},
		{"local_id": int64(2)},
		{"local_id": int64(3)},
	}
	got := sliceSearchRows(rows, 1, 1)
	if len(got) != 1 || rowInt64(got[0], "local_id") != 2 {
		t.Fatalf("sliceSearchRows = %#v", got)
	}
	if got := sliceSearchRows(rows, 5, 1); len(got) != 0 {
		t.Fatalf("sliceSearchRows past end = %#v", got)
	}
}

func TestCLISearchMessageRowCanTrimText(t *testing.T) {
	row := map[string]any{
		"local_id":            int64(1),
		"talker":              "room@chatroom",
		"talker_display_name": "Room",
		"chat_type":           "group",
		"create_time":         int64(1776330000),
		"sender_wxid":         "wxid_a",
		"sender_display_name": "Alice",
		"kind_name":           "text",
		"content":             "不是ABCDEFG而是HIJKLMN",
	}
	got := cliSearchMessageRow(row, map[string]any{"snippet_only": true, "max_text_chars": int64(4)})
	if got["text"] != "不是AB" || got["match"] != "不是AB" {
		t.Fatalf("trimmed row = %#v", got)
	}
	if got["text_truncated"] != true || got["original_text_chars"] == nil {
		t.Fatalf("truncation metadata = %#v", got)
	}
	got = cliSearchMessageRow(row, map[string]any{"include_text": false})
	if _, ok := got["text"]; ok {
		t.Fatalf("include_text=false leaked text: %#v", got)
	}
	if _, ok := got["match"]; ok {
		t.Fatalf("include_text=false leaked match: %#v", got)
	}
	if got["original_text_chars"] == nil {
		t.Fatalf("include_text=false should retain original_text_chars: %#v", got)
	}
}

func TestAgentMessageTextOutputOptionsCanTrimContextRows(t *testing.T) {
	rows := []map[string]any{
		{"id": map[string]any{"local_id": int64(1)}, "text": "不是ABCDEFG而是HIJKLMN"},
	}
	applyAgentTextOutputOptions(rows, map[string]any{"snippet_only": true, "max_text_chars": int64(4)})
	if rows[0]["text"] != "不是AB" {
		t.Fatalf("trimmed context text = %#v", rows[0])
	}
	if rows[0]["text_truncated"] != true || rows[0]["original_text_chars"] == nil {
		t.Fatalf("context truncation metadata = %#v", rows[0])
	}
	applyAgentTextOutputOptions(rows, map[string]any{"include_text": false})
	if _, ok := rows[0]["text"]; ok {
		t.Fatalf("include_text=false leaked context text: %#v", rows[0])
	}
}

func TestValidateSearchLimitAndMaxTextChars(t *testing.T) {
	if err := validateToolArgs("search", map[string]any{"keyword": "x", "limit": int64(1001)}); err == nil || !strings.Contains(err.Error(), "maximum is 1000") {
		t.Fatalf("search limit validation error = %v", err)
	}
	if err := validateToolArgs("search", map[string]any{"keyword": "x", "limit": int64(0)}); err == nil || !strings.Contains(err.Error(), "minimum is 1") {
		t.Fatalf("search limit minimum validation error = %v", err)
	}
	if err := validateToolArgs("search", map[string]any{"keyword": "x", "max_text_chars": int64(2001)}); err == nil || !strings.Contains(err.Error(), "maximum is 2000") {
		t.Fatalf("max_text_chars validation error = %v", err)
	}
	if err := validateToolArgs("search_with_context", map[string]any{"keyword": "x", "max_text_chars": int64(2001)}); err == nil || !strings.Contains(err.Error(), "maximum is 2000") {
		t.Fatalf("search_with_context max_text_chars validation error = %v", err)
	}
	if err := validateToolArgs("read_events", map[string]any{"limit": int64(1001)}); err == nil || !strings.Contains(err.Error(), "maximum is 1000") {
		t.Fatalf("read_events limit validation error = %v", err)
	}
	if err := validateToolArgs("read_events", map[string]any{"scan_limit": int64(5001)}); err == nil || !strings.Contains(err.Error(), "maximum is 5000") {
		t.Fatalf("read_events scan_limit validation error = %v", err)
	}
}

func TestSearchQueryMetaEchoesFromMe(t *testing.T) {
	meta := cliResultQueryMeta("search", "search", map[string]any{
		"keyword": "不是",
		"from_me": true,
		"limit":   int64(2),
	}, []map[string]any{{"local_id": int64(1)}})
	if meta["from_me"] != true {
		t.Fatalf("query meta did not echo from_me: %#v", meta)
	}
	meta = cliResultQueryMeta("search", "search", map[string]any{
		"keyword": "不是",
		"sender":  "self",
		"limit":   int64(2),
	}, []map[string]any{{"local_id": int64(1)}})
	if meta["from_me"] != true {
		t.Fatalf("query meta did not resolve sender=self: %#v", meta)
	}
}

func TestSearchSchemasExposeLimitBounds(t *testing.T) {
	doc, ok := cliHelpDocumentWithProfile("search", "").(map[string]any)
	if !ok {
		t.Fatalf("search doc type = %T", cliHelpDocumentWithProfile("search", ""))
	}
	tool, ok := doc["tool"].(toolDef)
	if !ok {
		t.Fatalf("search tool = %#v", doc["tool"])
	}
	props := toolInputProperties(tool)
	limit, _ := props["limit"].(map[string]any)
	if limit["minimum"] != int64(1) || limit["maximum"] != int64(1000) {
		t.Fatalf("search limit schema = %#v", limit)
	}
	maxText, _ := props["max_text_chars"].(map[string]any)
	if maxText["minimum"] != int64(1) || maxText["maximum"] != int64(2000) {
		t.Fatalf("max_text_chars schema = %#v", maxText)
	}

	doc, ok = cliHelpDocumentWithProfile("search-context", "").(map[string]any)
	if !ok {
		t.Fatalf("search-context doc type = %T", cliHelpDocumentWithProfile("search-context", ""))
	}
	tool, ok = doc["tool"].(toolDef)
	if !ok {
		t.Fatalf("search-context tool = %#v", doc["tool"])
	}
	props = toolInputProperties(tool)
	maxText, _ = props["max_text_chars"].(map[string]any)
	if maxText["minimum"] != int64(1) || maxText["maximum"] != int64(2000) {
		t.Fatalf("search-context max_text_chars schema = %#v", maxText)
	}
	if _, ok := props["include_text"]; !ok {
		t.Fatalf("search-context schema missing include_text: %#v", props)
	}
	if _, ok := props["snippet_only"]; !ok {
		t.Fatalf("search-context schema missing snippet_only: %#v", props)
	}
}

func TestTailSchemaExposesLimitBounds(t *testing.T) {
	doc, ok := cliHelpDocumentWithProfile("tail", "").(map[string]any)
	if !ok {
		t.Fatalf("tail doc type = %T", cliHelpDocumentWithProfile("tail", ""))
	}
	tool, ok := doc["tool"].(toolDef)
	if !ok {
		t.Fatalf("tail tool = %#v", doc["tool"])
	}
	props := toolInputProperties(tool)
	limit, _ := props["limit"].(map[string]any)
	if limit["minimum"] != int64(1) || limit["maximum"] != int64(1000) {
		t.Fatalf("tail limit schema = %#v", limit)
	}
	scanLimit, _ := props["scan_limit"].(map[string]any)
	if scanLimit["minimum"] != int64(1) || scanLimit["maximum"] != int64(5000) {
		t.Fatalf("tail scan_limit schema = %#v", scanLimit)
	}
}

func TestReadOSCapabilitiesSeparateLiveReadFromNameResolution(t *testing.T) {
	caps := readOSCapabilities(true, true, false)
	for _, key := range []string{"search", "sessions", "timeline", "context", "tail", "media"} {
		if !caps[key] {
			t.Fatalf("capability %s = false in live-read ready caps: %#v", key, caps)
		}
	}
	if caps["name_resolution"] {
		t.Fatalf("name_resolution should stay false without metadata index: %#v", caps)
	}
}

func TestReadRegressionScriptRequiresExplicitInputs(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "wechat-read-regression.sh")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("regression script unavailable: %v", err)
	}
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(),
		"WECHAT_CLI_BIN=true",
		"WECHAT_READ_TEST_CHAT=",
		"WECHAT_READ_TEST_KEYWORD=",
		"WECHAT_READ_TEST_OUT="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("regression script unexpectedly succeeded:\n%s", string(out))
	}
	if !strings.Contains(string(out), "set WECHAT_READ_TEST_CHAT and WECHAT_READ_TEST_KEYWORD explicitly") {
		t.Fatalf("regression script output = %s", string(out))
	}
}

func TestToolSchemaEnvelopeIdentifiesCommand(t *testing.T) {
	out := captureStdout(t, func() {
		runToolSchemaCLI([]string{"timeline"}, cliOptions{})
	})
	var env cliSuccessEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("tool-schema output is not success envelope: %v\n%s", err, string(out))
	}
	if !env.OK || env.Tool != "tool_schema" || env.Command != "tool-schema" {
		t.Fatalf("tool-schema envelope = %#v", env)
	}
	doc, ok := env.Data.(map[string]any)
	if !ok || doc["agent"] == nil || doc["command"] == nil || doc["tool"] == nil {
		t.Fatalf("tool-schema data = %#v", env.Data)
	}
}

func TestToolSchemaDefaultsToSlimProfileAndKeepsFullProfile(t *testing.T) {
	slimDoc, ok := cliHelpDocumentWithProfile("timeline", "").(map[string]any)
	if !ok {
		t.Fatalf("slim doc type = %T", cliHelpDocumentWithProfile("timeline", ""))
	}
	if slimDoc["schema_profile"] != "assistant" {
		t.Fatalf("schema_profile = %#v, want assistant", slimDoc["schema_profile"])
	}
	slimTool, ok := slimDoc["tool"].(toolDef)
	if !ok {
		t.Fatalf("slim tool = %#v", slimDoc["tool"])
	}
	slimProps := toolInputProperties(slimTool)
	for _, hidden := range []string{"before_message_local_id", "debug", "kind_name", "base_kind", "include_images"} {
		if _, ok := slimProps[hidden]; ok {
			t.Fatalf("slim schema still exposes %q: %#v", hidden, slimProps)
		}
	}
	for _, kept := range []string{"chat", "limit", "before_message", "after_message", "include_debug"} {
		if _, ok := slimProps[kept]; !ok {
			t.Fatalf("slim schema removed canonical %q: %#v", kept, slimProps)
		}
	}

	fullDoc, ok := cliHelpDocumentWithProfile("timeline", "all").(map[string]any)
	if !ok {
		t.Fatalf("full doc type = %T", cliHelpDocumentWithProfile("timeline", "all"))
	}
	if fullDoc["schema_profile"] != "all" {
		t.Fatalf("schema_profile = %#v, want all", fullDoc["schema_profile"])
	}
	fullTool, ok := fullDoc["tool"].(toolDef)
	if !ok {
		t.Fatalf("full tool = %#v", fullDoc["tool"])
	}
	fullProps := toolInputProperties(fullTool)
	for _, kept := range []string{"before_message_local_id", "debug", "kind_name", "base_kind", "include_images"} {
		if _, ok := fullProps[kept]; !ok {
			t.Fatalf("full schema missing %q: %#v", kept, fullProps)
		}
	}
}

func TestCLIHelpDocumentUnknownTarget(t *testing.T) {
	raw := cliHelpDocument("nope")
	env, ok := raw.(cliErrorEnvelope)
	if !ok {
		t.Fatalf("unknown help doc type = %T", raw)
	}
	if env.OK || env.Error.Code != "unknown_help_target" {
		t.Fatalf("unknown help env = %#v", env)
	}
}

func TestWriteJSONCLICompactByDefault(t *testing.T) {
	out := captureStdout(t, func() {
		writeJSONCLI(map[string]any{"ok": true}, cliOptions{})
	})
	got := string(out)
	if got != "{\"ok\":true}\n" {
		t.Fatalf("compact JSON = %q", got)
	}
}

func TestCLIAgentDataEnvelopeWrapsSearchRows(t *testing.T) {
	rows := []wcdb.Row{{
		"local_id":            int64(26),
		"talker":              "room@chatroom",
		"create_time":         int64(1779443475),
		"sender_wxid":         "wxid_bob",
		"sender_display_name": "Bob",
		"kind_name":           "solitaire",
		"content_summary":     "测试群接龙",
		"content":             "#接龙\n测试群接龙",
	}}

	data := cliAgentDataEnvelope("search", "search", map[string]any{"keyword": "测试", "limit": int64(10)}, rows)
	env, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("data = %T, want map", data)
	}
	query, ok := env["query"].(map[string]any)
	if !ok || query["keyword"] != "测试" || query["returned"] != 1 || query["has_more"] != false {
		t.Fatalf("query = %#v", env["query"])
	}
	messages, ok := env["messages"].([]map[string]any)
	if !ok || len(messages) != 1 || messages[0]["kind"] != "solitaire" || messages[0]["text"] != "测试群接龙" || messages[0]["match"] != "#接龙\n测试群接龙" {
		t.Fatalf("messages = %#v", env["messages"])
	}
	id, ok := messages[0]["id"].(map[string]any)
	if !ok || id["local_id"] != int64(26) || id["talker"] != "room@chatroom" {
		t.Fatalf("id = %#v", messages[0]["id"])
	}
}

func TestCLIErrorCodeMapping(t *testing.T) {
	cases := map[string]string{
		`missing required argument "keyword" for tool search`: "missing_required_argument",
		`unknown argument "json" for tool resolve_chat`:       "unknown_argument",
		`invalid argument "limit" for tool sessions`:          "invalid_argument",
	}
	for msg, want := range cases {
		if got := cliErrorCode(errors.New(msg)); got != want {
			t.Fatalf("cliErrorCode(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestCLIErrorAdviceForKeywordAndUnknownCommand(t *testing.T) {
	kw := cliErrorAdvice("tool_error", "keyword is required", "search", "search")
	if kw.NextAction == "" || len(kw.SuggestedCommands) == 0 {
		t.Fatalf("keyword advice missing: %#v", kw)
	}
	if !strings.Contains(strings.Join(kw.SuggestedCommands, "\n"), "search-context") {
		t.Fatalf("keyword advice should suggest search-context: %#v", kw)
	}
	unknown := cliErrorAdvice("unknown_command", `unknown command "foo"`, "", "foo")
	if unknown.NextAction == "" || !strings.Contains(strings.Join(unknown.SuggestedCommands, "\n"), "agent") {
		t.Fatalf("unknown command advice = %#v", unknown)
	}
}

func TestCLIToolCoverage(t *testing.T) {
	missing := map[string]bool{}
	for _, td := range toolDefs {
		missing[td.Name] = true
	}
	for _, name := range cliToolNames() {
		delete(missing, name)
	}
	if len(missing) > 0 {
		t.Fatalf("CLI does not cover tools: %#v", missing)
	}
}

func TestCLICommandSpecsHaveAgentExamples(t *testing.T) {
	for _, spec := range cliCommandSpecs {
		if len(spec.Examples) == 0 {
			t.Fatalf("command %q has no agent examples", spec.Command)
		}
	}
}

func TestParseKVFlags(t *testing.T) {
	got := parseKVFlags([]string{"--chat", "某群", "--limit=20", "--include-debug", "--server-id-str", "9223372036854775808"})
	if got["chat"] != "某群" || got["limit"] != int64(20) || got["include_debug"] != true || got["server_id_str"] != "9223372036854775808" {
		t.Fatalf("parseKVFlags = %#v", got)
	}
}

func TestParseKVFlagsAgentBooleans(t *testing.T) {
	got := parseKVFlags([]string{"--include-status", "--include-anchor=false"})
	if got["include_status"] != true || got["include_anchor"] != false {
		t.Fatalf("parseKVFlags booleans = %#v", got)
	}
}

func TestContextAnchorRefFromArgs(t *testing.T) {
	ref, err := contextAnchorRefFromArgs(map[string]any{"server_id_str": "9223372036854775807"})
	if err != nil {
		t.Fatalf("contextAnchorRefFromArgs returned error: %v", err)
	}
	if ref.ServerID != 9223372036854775807 || ref.Kind != "server_id_str" {
		t.Fatalf("ref = %#v", ref)
	}
	if _, err := contextAnchorRefFromArgs(map[string]any{"local_id": int64(0)}); err == nil {
		t.Fatal("contextAnchorRefFromArgs accepted local_id=0")
	}
}

func TestContextCountArgCapsWindow(t *testing.T) {
	got := contextCountArg(map[string]any{"before_count": int64(999)}, "before_count", "before_messages", 20)
	if got != 500 {
		t.Fatalf("contextCountArg = %d, want 500", got)
	}
}

func TestParseUpdateArgs(t *testing.T) {
	got, err := parseUpdateArgs([]string{"--dry-run", "--tag", "v1.2.3", "--asset=wechat-cli-test.zip", "--repo", "r266-tech/wechat-cli"})
	if err != nil {
		t.Fatalf("parseUpdateArgs error: %v", err)
	}
	if !got.DryRun || got.Tag != "v1.2.3" || got.Asset != "wechat-cli-test.zip" || got.Repo != "r266-tech/wechat-cli" {
		t.Fatalf("parseUpdateArgs = %#v", got)
	}
}

func TestParseUpdateArgsRejectsUnknown(t *testing.T) {
	if _, err := parseUpdateArgs([]string{"--nope"}); err == nil {
		t.Fatal("parseUpdateArgs accepted unknown flag")
	}
}

func TestBoolKVFlagsDoNotConsumePositionals(t *testing.T) {
	args := []string{"--include-debug", "某群"}
	got := parseKVFlags(args)
	if got["include_debug"] != true {
		t.Fatalf("include_debug = %#v, want true", got["include_debug"])
	}
	if pos := firstPositional(args); pos != "某群" {
		t.Fatalf("firstPositional = %q, want 某群", pos)
	}
}

func TestContentSummary_Text(t *testing.T) {
	got := contentSummary(1, 0, "wxid_x:\nhello world", nil)
	if got != "hello world" {
		t.Errorf("text = %q, want 'hello world'", got)
	}
}

func TestContentSummary_Image(t *testing.T) {
	got := contentSummary(3, 0, "<msg>...", nil)
	if got != "[图片]" {
		t.Errorf("image = %q, want '[图片]'", got)
	}
}

func TestContentSummary_AppNoTitle(t *testing.T) {
	got := contentSummary(49, 0, "<msg>...", nil)
	if got != "[应用消息]" {
		t.Errorf("app no parsed = %q, want '[应用消息]'", got)
	}
}

func TestContentSummary_AppWithTitle(t *testing.T) {
	parsed := map[string]any{"title": "微信转账"}
	got := contentSummary(49, 0, "<msg>...", parsed)
	if got != "微信转账" {
		t.Errorf("app with title = %q, want '微信转账'", got)
	}
}

func TestContentSummary_Quote(t *testing.T) {
	parsed := map[string]any{
		"title": "好的",
		"refermsg": map[string]any{
			"type":        int(1),
			"content_raw": "原话",
		},
	}
	got := contentSummary(49, 57, "<msg>...", parsed)
	if !strings.HasPrefix(got, "[引用: ") {
		t.Errorf("quote should start with '[引用: ', got %q", got)
	}
	if !strings.Contains(got, "好的") {
		t.Errorf("quote should include reply title '好的', got %q", got)
	}
}

func TestContentSummary_System(t *testing.T) {
	got := contentSummary(10000, 0, "对方撤回了一条消息", nil)
	if got != "对方撤回了一条消息" {
		t.Errorf("system = %q", got)
	}
}

func TestLiteMessagesKeepsAgentContext(t *testing.T) {
	rows := []wcdb.Row{{
		"talker":              "wxid_a",
		"talker_display_name": "Alice",
		"chat_type":           "private",
		"local_id":            int64(1),
		"server_id":           int64(7710666891970547832),
		"server_id_str":       "7710666891970547832",
		"content_summary":     "hello",
		"message_content":     "raw body",
	}}

	got := liteMessages(rows, "lite")
	for _, key := range []string{"talker", "talker_display_name", "chat_type", "local_id", "server_id", "server_id_str", "content_summary"} {
		if _, ok := got[0][key]; !ok {
			t.Fatalf("liteMessages removed %q", key)
		}
	}
	if _, ok := got[0]["message_content"]; ok {
		t.Fatalf("liteMessages kept raw message_content")
	}
}

func TestIncludeMediaPathsForMessagesDefaultsOn(t *testing.T) {
	if !includeMediaPathsForMessages(nil) {
		t.Fatalf("include_media_paths should default to true for messages")
	}
	if !includeMediaPathsForMessages(map[string]any{}) {
		t.Fatalf("missing include_media_paths should default to true")
	}
	if includeMediaPathsForMessages(map[string]any{"include_media_paths": false}) {
		t.Fatalf("include_media_paths=false should disable message media enrichment")
	}
	if !includeMediaPathsForMessages(map[string]any{"include_media_paths": true}) {
		t.Fatalf("include_media_paths=true should enable message media enrichment")
	}
}

func TestLiteMessagesHidesDebugAndAddsDisplayRefs(t *testing.T) {
	row := wcdb.Row{
		"talker":              "wxid_a",
		"talker_display_name": "Alice",
		"local_id":            int64(1),
		"create_time_human":   "2026-05-22 15:08:22",
		"sender_display_name": "V",
		"kind_name":           "image",
		"content_summary":     "[图片]",
		"media_local_paths":   []string{"/tmp/direct.png"},
		"media_resources":     []map[string]any{{"resource_family": "image", "resource_id": int64(7)}},
		"media_read_hints": []map[string]any{{
			"source":                      "message_resource",
			"address_type":                "local_file",
			"resource_family":             "image",
			"direct_readable_local_paths": []string{"/tmp/direct.png"},
			"local_path_details": []map[string]any{{
				"path":            "/tmp/direct.png",
				"uri":             localFileURI("/tmp/direct.png"),
				"direct_readable": true,
				"width":           10,
				"height":          20,
			}},
		}},
	}

	got := liteMessages([]wcdb.Row{copyRow(row)}, "lite")[0]
	if _, ok := got["media_read_hints"]; ok {
		t.Fatalf("liteMessages leaked media_read_hints by default: %#v", got)
	}
	if _, ok := got["media_resources"]; ok {
		t.Fatalf("liteMessages leaked media_resources by default: %#v", got)
	}
	images, ok := got["images"].([]map[string]any)
	if !ok || len(images) != 1 || images[0]["path"] != "/tmp/direct.png" || len(images[0]) != 1 {
		t.Fatalf("lite image refs = %#v", got["images"])
	}
	display, ok := got["display"].(map[string]any)
	if !ok || display["speaker"] != "V" || display["display_text"] != "[图片]" {
		t.Fatalf("display = %#v", got["display"])
	}
	render, ok := display["render"].([]map[string]any)
	if !ok || len(render) != 1 || render[0]["type"] != "image" || render[0]["path"] != "/tmp/direct.png" {
		t.Fatalf("display render = %#v", display["render"])
	}

	debug := liteMessages([]wcdb.Row{copyRow(row)}, "lite", true)[0]
	if _, ok := debug["media_read_hints"]; !ok {
		t.Fatalf("includeDebug should keep media_read_hints: %#v", debug)
	}
	if _, ok := debug["media_resources"]; !ok {
		t.Fatalf("includeDebug should keep media_resources: %#v", debug)
	}
}

func copyRow(r wcdb.Row) wcdb.Row {
	out := wcdb.Row{}
	for k, v := range r {
		out[k] = v
	}
	return out
}

func TestAgentMessagesFlattensImagesAndQuotes(t *testing.T) {
	rows := []wcdb.Row{
		{
			"talker":              "45428231044@chatroom",
			"local_id":            int64(682),
			"server_id_str":       "2369251559671996886",
			"create_time":         int64(1779433702),
			"create_time_human":   "2026-05-22 15:08:22",
			"sender_display_name": "V",
			"sender_wxid":         "wxid_v",
			"is_from_me":          true,
			"kind_name":           "image",
			"content_summary":     "[图片]",
			"media_read_hints": []map[string]any{{
				"source":                          "message_resource",
				"address_type":                    "local_file",
				"resource_family":                 "image",
				"direct_readable_local_paths":     []string{"/tmp/direct.png"},
				"decoded_local_paths":             []string{"/tmp/decoded.png"},
				"local_paths":                     []string{"/tmp/direct.png", "/tmp/raw.dat"},
				"direct_readable_local_path_uris": []string{"file:///tmp/direct.png"},
			}},
		},
		{
			"create_time_human":   "2026-05-22 15:11:08",
			"sender_display_name": "V",
			"kind_name":           "quote",
			"content_summary":     "[引用: [图片]] 结果真来啊",
			"message_content_parsed": map[string]any{
				"title": "结果真来啊",
				"refermsg": map[string]any{
					"type":        3,
					"createtime":  int64(1779433686),
					"displayname": "V",
					"content_raw": "<msg><img /></msg>",
					"content_parsed": map[string]any{
						"md5": "54fe6e6b16a75406b445038b197fff54",
					},
				},
			},
			"media_read_hints": []map[string]any{{
				"source":                      "message_refermsg",
				"message_role":                "referenced_message",
				"address_type":                "local_file",
				"resource_family":             "image",
				"direct_readable_local_paths": []string{"/tmp/quoted.png"},
			}},
		},
	}

	got := agentMessages(rows)
	if len(got) != 2 {
		t.Fatalf("agentMessages len = %d, want 2", len(got))
	}
	if _, ok := got[0]["media_read_hints"]; ok {
		t.Fatalf("agent view leaked media_read_hints: %#v", got[0])
	}
	id, ok := got[0]["id"].(map[string]any)
	if !ok || id["local_id"] != int64(682) || id["server_id_str"] != "2369251559671996886" || id["talker"] != "45428231044@chatroom" {
		t.Fatalf("id = %#v, want stable local/server ids", got[0]["id"])
	}
	if got[0]["sender_wxid"] != "wxid_v" || got[0]["is_from_me"] != true {
		t.Fatalf("sender identity = %#v/%#v", got[0]["sender_wxid"], got[0]["is_from_me"])
	}
	if got[0]["create_time"] != int64(1779433702) || got[0]["time_iso"] == "" {
		t.Fatalf("machine time = %#v/%#v", got[0]["create_time"], got[0]["time_iso"])
	}
	images, ok := got[0]["images"].([]map[string]any)
	if !ok || len(images) != 1 || images[0]["path"] != "/tmp/direct.png" || len(images[0]) != 1 {
		t.Fatalf("agent image paths = %#v, want direct path only", got[0]["images"])
	}
	if got[1]["text"] != "结果真来啊" {
		t.Fatalf("quote reply text = %#v, want title only", got[1]["text"])
	}
	quote, ok := got[1]["quote"].(map[string]any)
	if !ok {
		t.Fatalf("quote = %#v, want map", got[1]["quote"])
	}
	if quote["kind"] != "image" || quote["text"] != "[图片]" {
		t.Fatalf("quote summary = %#v", quote)
	}
	quoteImages, ok := quote["images"].([]map[string]any)
	if !ok || len(quoteImages) != 1 || quoteImages[0]["path"] != "/tmp/quoted.png" {
		t.Fatalf("quote images = %#v, want quoted image path", quote["images"])
	}
}

func TestAgentMessagesReusesWindowMediaForQuotes(t *testing.T) {
	rows := []wcdb.Row{
		{
			"talker":              "room@chatroom",
			"local_id":            int64(13),
			"server_id_str":       "6141378114596572979",
			"create_time":         int64(1779442793),
			"kind_name":           "file",
			"content_summary":     "paper.pdf",
			"sender_display_name": "V",
			"media_read_hints": []map[string]any{{
				"source":                      "message_resource",
				"address_type":                "local_file",
				"resource_family":             "file",
				"direct_readable_local_paths": []string{"/tmp/paper.pdf"},
				"local_path_details": []map[string]any{{
					"path":      "/tmp/paper.pdf",
					"file_size": int64(1234),
				}},
			}},
			"media_resources": []map[string]any{{
				"resource_family": "file",
				"file_name":       "paper.pdf",
			}},
		},
		{
			"talker":              "room@chatroom",
			"local_id":            int64(28),
			"server_id_str":       "785813827353786291",
			"create_time":         int64(1779443506),
			"sender_display_name": "Bob",
			"kind_name":           "quote",
			"content_summary":     "[引用: paper.pdf] 值得学习",
			"message_content_parsed": map[string]any{
				"title": "值得学习",
				"refermsg": map[string]any{
					"type":        49,
					"createtime":  int64(1779442793),
					"displayname": "V",
					"svrid":       "6141378114596572979",
					"content_raw": "<msg><appmsg><type>6</type><title>paper.pdf</title></appmsg></msg>",
					"content_parsed": map[string]any{
						"app_subtype": int64(6),
						"title":       "paper.pdf",
						"app_attach": map[string]any{
							"file_name": "paper.pdf",
							"file_ext":  "pdf",
							"total_len": int64(1234),
						},
					},
				},
			},
		},
	}

	got := agentMessages(rows)
	quote := got[1]["quote"].(map[string]any)
	files := quote["files"].([]map[string]any)
	if len(files) != 1 || files[0]["path"] != "/tmp/paper.pdf" || files[0]["readable"] == false {
		t.Fatalf("quote files = %#v, want readable file path from source message", files)
	}
	sourceID := quote["source_id"].(map[string]any)
	if sourceID["local_id"] != int64(13) || sourceID["server_id_str"] != "6141378114596572979" {
		t.Fatalf("quote source_id = %#v", sourceID)
	}
}

func TestAgentMessagesWarnsOnRawImageFallback(t *testing.T) {
	rows := []wcdb.Row{{
		"create_time_human":   "2026-05-22 15:08:22",
		"sender_display_name": "V",
		"kind_name":           "image",
		"content_summary":     "[图片]",
		"media_read_hints": []map[string]any{{
			"source":          "message_resource",
			"address_type":    "local_file",
			"resource_family": "image",
			"local_paths":     []string{"/tmp/raw.dat"},
			"local_path_details": []map[string]any{{
				"path":          "/tmp/raw.dat",
				"decode_status": "needs_image_key",
			}},
		}},
	}}

	got := agentMessages(rows)
	if _, ok := got[0]["images"]; ok {
		t.Fatalf("raw fallback images = %#v, want no unreadable image path", got[0]["images"])
	}
	warnings, ok := got[0]["warnings"].([]string)
	if !ok || len(warnings) != 2 || warnings[0] != "image_decode_needs_image_key" || warnings[1] != "image_paths_not_direct_readable" {
		t.Fatalf("warnings = %#v, want concise decode + readability warnings", got[0]["warnings"])
	}
}

func TestAgentReadyMediaResourcesExposeOnlyReadableImagePath(t *testing.T) {
	items := []map[string]any{{
		"id":                    map[string]any{"local_id": int64(7), "talker": "room@chatroom"},
		"time_iso":              "2026-06-04T16:00:00+08:00",
		"kind":                  "image",
		"sender":                "V",
		"kind_name":             "image",
		"base_kind":             int64(3),
		"message_origin_source": int64(1),
		"media_local_paths":     []string{"/tmp/raw.dat", "/tmp/decoded.png"},
		"media_read_hints": []map[string]any{{
			"source":                      "message_resource",
			"address_type":                "local_file",
			"resource_family":             "image",
			"direct_readable":             true,
			"direct_readable_local_paths": []string{"/tmp/decoded.png"},
			"local_paths":                 []string{"/tmp/raw.dat", "/tmp/decoded.png"},
			"local_path_details": []map[string]any{{
				"path":          "/tmp/raw.dat",
				"decode_status": "decoded",
				"decoded_path":  "/tmp/decoded.png",
			}},
		}},
		"resources": []map[string]any{{
			"resource_id":                 int64(11),
			"resource_family":             "image",
			"resource_type_raw":           int64(65537),
			"variant_code":                int64(1),
			"size":                        int64(123),
			"status":                      int64(1),
			"direct_readable":             true,
			"direct_readable_local_paths": []string{"/tmp/decoded.png"},
			"local_paths":                 []string{"/tmp/raw.dat", "/tmp/decoded.png"},
			"local_path_details": []map[string]any{{
				"path":          "/tmp/raw.dat",
				"decode_status": "decoded",
				"decoded_path":  "/tmp/decoded.png",
			}},
		}},
	}}

	agentReadyMediaResourceOutput(items)
	images, ok := items[0]["images"].([]map[string]any)
	if !ok || len(images) != 1 || images[0]["path"] != "/tmp/decoded.png" || len(images[0]) != 1 {
		t.Fatalf("images = %#v, want single readable path", items[0]["images"])
	}
	for _, key := range []string{"media_local_paths", "media_read_hints", "base_kind", "message_origin_source"} {
		if _, ok := items[0][key]; ok {
			t.Fatalf("agent-ready media item leaked %s: %#v", key, items[0])
		}
	}
	resources, ok := items[0]["resources"].([]map[string]any)
	if !ok || len(resources) != 1 {
		t.Fatalf("resources = %#v, want one resource", items[0]["resources"])
	}
	if resources[0]["path"] != "/tmp/decoded.png" {
		t.Fatalf("resource summary = %#v, want readable path", resources[0])
	}
	if items[0]["id"] == nil || items[0]["time_iso"] != "2026-06-04T16:00:00+08:00" || items[0]["kind"] != "image" || items[0]["sender"] != "V" {
		t.Fatalf("message reference fields were not preserved: %#v", items[0])
	}
	for _, key := range []string{"local_paths", "local_path_details", "resource_type_raw", "variant_code", "resource_id", "resource_family", "status", "direct_readable", "paths"} {
		if _, ok := resources[0][key]; ok {
			t.Fatalf("agent-ready resource leaked %s: %#v", key, resources[0])
		}
	}
}

func TestAgentReadyMediaResourcesHideUnreadableImageDAT(t *testing.T) {
	items := []map[string]any{{
		"kind_name":         "image",
		"media_local_paths": []string{"/tmp/raw.dat"},
		"media_read_hints": []map[string]any{{
			"source":          "message_resource",
			"address_type":    "local_file",
			"resource_family": "image",
			"direct_readable": false,
			"local_paths":     []string{"/tmp/raw.dat"},
			"local_path_details": []map[string]any{{
				"path":          "/tmp/raw.dat",
				"decode_status": "needs_image_key",
			}},
		}},
		"resources": []map[string]any{{
			"resource_id":     int64(11),
			"resource_family": "image",
			"size":            int64(123),
			"status":          int64(1),
			"direct_readable": false,
			"local_paths":     []string{"/tmp/raw.dat"},
			"local_path_details": []map[string]any{{
				"path":          "/tmp/raw.dat",
				"decode_status": "needs_image_key",
			}},
		}},
	}}

	agentReadyMediaResourceOutput(items)
	if _, ok := items[0]["images"]; ok {
		t.Fatalf("unreadable .dat should not become image path: %#v", items[0]["images"])
	}
	warnings, ok := items[0]["warnings"].([]string)
	if !ok || len(warnings) != 2 || warnings[0] != "image_decode_needs_image_key" || warnings[1] != "image_paths_not_direct_readable" {
		t.Fatalf("item warnings = %#v, want missing key + unreadable path", items[0]["warnings"])
	}
	resources := items[0]["resources"].([]map[string]any)
	if _, ok := resources[0]["path"]; ok {
		t.Fatalf("unreadable resource leaked path: %#v", resources[0])
	}
	if _, ok := resources[0]["local_paths"]; ok {
		t.Fatalf("unreadable resource leaked raw local_paths: %#v", resources[0])
	}
	resWarnings, ok := resources[0]["warnings"].([]string)
	if !ok || len(resWarnings) != 2 || resWarnings[0] != "image_decode_needs_image_key" || resWarnings[1] != "image_paths_not_direct_readable" {
		t.Fatalf("resource warnings = %#v, want missing key + unreadable path", resources[0]["warnings"])
	}
}

func TestAgentMessagesFlattensFiles(t *testing.T) {
	rows := []wcdb.Row{{
		"create_time_human":   "2026-05-22 15:08:22",
		"sender_display_name": "V",
		"kind_name":           "file",
		"content_summary":     "[文件] report.pdf",
		"media_resources": []map[string]any{{
			"resource_family": "file",
			"file_name":       "report.pdf",
		}},
		"media_read_hints": []map[string]any{{
			"source":                      "message_resource",
			"address_type":                "local_file",
			"resource_family":             "file",
			"direct_readable_local_paths": []string{"/tmp/report.pdf", "/tmp/report(1).pdf"},
			"local_path_details": []map[string]any{{
				"path":            "/tmp/report.pdf",
				"uri":             localFileURI("/tmp/report.pdf"),
				"direct_readable": true,
				"file_size":       int64(123),
			}, {
				"path":            "/tmp/report(1).pdf",
				"uri":             localFileURI("/tmp/report(1).pdf"),
				"direct_readable": true,
				"file_size":       int64(123),
			}},
		}},
	}}

	got := agentMessages(rows)
	files, ok := got[0]["files"].([]map[string]any)
	if !ok || len(files) != 1 || files[0]["path"] != "/tmp/report.pdf" || files[0]["name"] != "report.pdf" || files[0]["file_size"] != int64(123) {
		t.Fatalf("files = %#v, want de-duplicated display-ready file ref", got[0]["files"])
	}
	if _, ok := got[0]["images"]; ok {
		t.Fatalf("file message should not expose top-level images: %#v", got[0])
	}
}

func TestAgentMessagesSeparatesVideoFileAndCover(t *testing.T) {
	rows := []wcdb.Row{{
		"kind_name":       "video",
		"content_summary": "[视频]",
		"media_read_hints": []map[string]any{{
			"source":            "message_resource",
			"address_type":      "local_file",
			"resource_families": []string{"video", "cover"},
			"direct_readable_local_paths": []string{
				"/tmp/sample.mp4",
				"/tmp/sample_thumb.jpg",
			},
			"local_paths": []string{
				"/tmp/sample.mp4",
				"/tmp/sample_thumb.jpg",
			},
		}},
	}}

	got := agentMessages(rows)
	if _, ok := got[0]["images"]; ok {
		t.Fatalf("video message should not expose cover as top-level images: %#v", got[0]["images"])
	}
	videos, ok := got[0]["videos"].([]map[string]any)
	if !ok || len(videos) != 1 || videos[0]["path"] != "/tmp/sample.mp4" {
		t.Fatalf("videos = %#v, want only mp4 path", got[0]["videos"])
	}
	video, ok := got[0]["video"].(map[string]any)
	if !ok {
		t.Fatalf("video payload = %#v, want map", got[0]["video"])
	}
	covers, ok := video["cover_images"].([]map[string]any)
	if !ok || len(covers) != 1 || covers[0]["path"] != "/tmp/sample_thumb.jpg" {
		t.Fatalf("cover_images = %#v, want thumbnail path", video["cover_images"])
	}
}

func TestAgentMessagesExposesVoiceTranscript(t *testing.T) {
	rows := []wcdb.Row{{
		"kind_name":       "voice",
		"content_summary": "[语音] 3.3s",
		"message_content_parsed": map[string]any{
			"duration_ms":  int64(3260),
			"voice_format": int64(4),
			"length":       int64(5824),
		},
		"media_read_hints": []map[string]any{{
			"source":                      "media_0_voiceinfo",
			"address_type":                "local_file",
			"resource_family":             "voice",
			"direct_readable_local_paths": []string{"/tmp/voice.silk"},
			"local_path_details": []map[string]any{{
				"path":            "/tmp/voice.silk",
				"uri":             localFileURI("/tmp/voice.silk"),
				"direct_readable": true,
				"mime_type":       "audio/silk",
				"file_size":       int64(5824),
			}},
			"transcript": map[string]any{
				"status": "ok",
				"text":   "明天十点开会",
				"engine": "local_asr",
				"model":  "test-model",
			},
		}},
	}}

	got := agentMessages(rows)
	if got[0]["text"] != "[语音] 明天十点开会" {
		t.Fatalf("message text = %#v, want transcript-backed voice text", got[0]["text"])
	}
	voice, ok := got[0]["voice"].(map[string]any)
	if !ok {
		t.Fatalf("voice payload = %#v, want map", got[0]["voice"])
	}
	if _, ok := voice["audio"]; ok {
		t.Fatalf("voice audio leaked into default agent payload: %#v", voice)
	}
	transcript, ok := voice["transcript"].(map[string]any)
	if !ok || transcript["status"] != "ok" || transcript["text"] != "明天十点开会" || transcript["engine"] != "local_asr" {
		t.Fatalf("voice transcript = %#v", voice["transcript"])
	}
	if voice["duration_ms"] != int64(3260) {
		t.Fatalf("voice metadata = %#v", voice)
	}
}

func TestVoiceTranscriptUsesConfiguredLocalASRAndCache(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "voice.wav")
	if err := os.WriteFile(wav, []byte("RIFF0000WAVEfmt "), 0o600); err != nil {
		t.Fatal(err)
	}
	asr := buildTestBinary(t, dir, "asr", `package main

import "fmt"

func main() {
	fmt.Println("本地转写成功")
}
`)
	t.Setenv("WX_MCP_VOICE_TRANSCRIBE_CMD", shellQuote(asr)+" {audio}")

	srv := &server{}
	got := srv.voiceTranscriptForAgent(wav)
	if got["status"] != "ok" || got["text"] != "本地转写成功" || got["engine"] != "custom" {
		t.Fatalf("transcript = %#v", got)
	}
	t.Setenv("WX_MCP_VOICE_TRANSCRIBE_CMD", "false {audio}")
	cached := srv.voiceTranscriptForAgent(wav)
	if cached["status"] != "ok" || cached["text"] != "本地转写成功" {
		t.Fatalf("cached transcript = %#v", cached)
	}
}

func TestVoiceTranscriptStrictReadOnlyDoesNotWriteCache(t *testing.T) {
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
	dir := t.TempDir()
	wav := filepath.Join(dir, "voice.wav")
	if err := os.WriteFile(wav, []byte("RIFF0000WAVEfmt "), 0o600); err != nil {
		t.Fatal(err)
	}
	cachePath := strings.TrimSuffix(wav, filepath.Ext(wav)) + ".transcript.json"

	got := (&server{}).voiceTranscriptForAgent(wav)
	if got["status"] != "unavailable" || got["engine"] != "disabled" || got["reason"] != "strict_read_only" {
		t.Fatalf("transcript = %#v", got)
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict read-only wrote transcript cache or unexpected stat error: %v", err)
	}
}

func TestVoiceTranscriptPrefersFasterWhisperLargeV3AndRefreshesOldCache(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "voice.wav")
	if err := os.WriteFile(wav, []byte("RIFF0000WAVEfmt "), 0o600); err != nil {
		t.Fatal(err)
	}
	oldCache := strings.TrimSuffix(wav, filepath.Ext(wav)) + ".transcript.json"
	if err := os.WriteFile(oldCache, []byte(`{"status":"no_speech","engine":"whisper.cpp","model":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	python := buildTestBinary(t, dir, "python3", `package main

import (
	"fmt"
	"os"
)

func main() {
	args := os.Args
	if len(args) == 3 && args[1] == "-c" && args[2] == "import faster_whisper" {
		return
	}
	if len(args) > 5 {
		_ = os.WriteFile(args[0]+".args.log", []byte(args[4]+"/"+args[5]+"\n"), 0o600)
		fmt.Println("大模型转写成功")
	}
}
`)
	if _, err := os.Stat(python + ".args.log"); err == nil {
		t.Fatalf("unexpected stale args log at %s", python+".args.log")
	}
	t.Setenv("WX_MCP_FASTER_WHISPER_PYTHON", python)
	t.Setenv("WX_MCP_FASTER_WHISPER_MODEL", "")
	t.Setenv("WX_MCP_FASTER_WHISPER_LANGUAGE", "zh")
	t.Setenv("WX_MCP_VOICE_TRANSCRIBE_CMD", "")

	srv := &server{}
	got := srv.voiceTranscriptForAgent(wav)
	if got["status"] != "ok" || got["text"] != "大模型转写成功" || got["engine"] != "faster-whisper" || got["model"] != "large-v3" {
		t.Fatalf("transcript = %#v", got)
	}
	if version, ok := integerArgValue(got["cache_version"]); !ok || version != voiceTranscriptCacheVersion {
		t.Fatalf("cache version = %#v, want %d", got["cache_version"], voiceTranscriptCacheVersion)
	}
	args, err := os.ReadFile(python + ".args.log")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(args)) != "large-v3/zh" {
		t.Fatalf("faster-whisper args = %q, want large-v3/zh", strings.TrimSpace(string(args)))
	}
}

func TestSplitWindowsCommandLine(t *testing.T) {
	got, ok := splitWindowsCommandLine(`"C:\Program Files\wechat-cli\asr.exe" --input "{audio}"`)
	if !ok {
		t.Fatal("splitWindowsCommandLine returned !ok")
	}
	want := []string{`C:\Program Files\wechat-cli\asr.exe`, "--input", "{audio}"}
	if len(got) != len(want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %#v, want %#v", got, want)
		}
	}
	if _, ok := splitWindowsCommandLine(`"unterminated`); ok {
		t.Fatal("splitWindowsCommandLine accepted unterminated quote")
	}
}

func buildTestBinary(t *testing.T, dir, name, source string) string {
	t.Helper()
	src := filepath.Join(dir, name+".go")
	if err := os.WriteFile(src, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, src)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, data)
	}
	return out
}

func TestAgentPayloadHidesProtocolFieldsByDefault(t *testing.T) {
	rows := []wcdb.Row{
		{
			"kind_name":       "link",
			"content_summary": "Article",
			"message_content_parsed": map[string]any{
				"title":               "Article",
				"url":                 "https://example.com",
				"source_display_name": "Source",
				"thumb_url":           "https://example.com/thumb.jpg",
				"app_subtype":         int64(5),
			},
		},
		{
			"kind_name":       "red_packet",
			"content_summary": "恭喜发财",
			"message_content_parsed": map[string]any{
				"wcpayinfo": map[string]any{
					"sendertitle":   "恭喜发财",
					"receivertitle": "已领取",
					"scenetext":     "微信红包",
					"nativeurl":     "wxpay://secret",
				},
			},
		},
		{
			"kind_name":       "card",
			"content_summary": "[名片] V",
			"message_content_parsed": map[string]any{
				"username":           "wxid_v",
				"nickname":           "V",
				"small_head_img_url": "https://avatar/small",
				"big_head_img_url":   "https://avatar/big",
				"province":           "Beijing",
				"city":               "Chaoyang",
				"sex":                int64(1),
				"scene":              int64(17),
			},
		},
	}

	got := agentMessages(rows)
	link := got[0]["link"].(map[string]any)
	if _, ok := link["app_subtype"]; ok || link["thumb_url"] != "https://example.com/thumb.jpg" {
		t.Fatalf("link payload = %#v", link)
	}
	redPacket := got[1]["red_packet"].(map[string]any)
	if _, ok := redPacket["native_url"]; ok || redPacket["title"] != "恭喜发财" {
		t.Fatalf("red packet payload = %#v", redPacket)
	}
	card := got[2]["card"].(map[string]any)
	if card["gender"] != "male" || card["sex_code"] != int64(1) || card["source_scene_code"] != int64(17) {
		t.Fatalf("card payload = %#v", card)
	}
}

func TestAgentMessagesSummarizesSystemXML(t *testing.T) {
	rows := []wcdb.Row{{
		"create_time_human": "2026-05-22 16:06:33",
		"kind_name":         "system",
		"content_summary":   `<?xml version="1.0"?><sysmsg type="revokemsg"><revokemsg><content>"赵桐" 撤回了一条消息</content></revokemsg></sysmsg>`,
	}}

	got := agentMessages(rows)
	if got[0]["sender"] != "系统" {
		t.Fatalf("system sender = %#v, want 系统", got[0]["sender"])
	}
	if got[0]["text"] != `"赵桐" 撤回了一条消息` {
		t.Fatalf("system text = %#v, want revoke content", got[0]["text"])
	}
}

func TestAgentMessagesStructuresCommonNonTextPayloads(t *testing.T) {
	rows := []wcdb.Row{
		{
			"kind_name":       "link",
			"content_summary": "一文搞懂如何在Codex中使用goals",
			"message_content_parsed": map[string]any{
				"title":               "一文搞懂如何在Codex中使用goals",
				"url":                 "https://mp.weixin.qq.com/s/q7WlpIORMaL1C7YATwK4zQ",
				"source_display_name": "AI寒武纪",
				"thumb_url":           "https://example.test/thumb.jpg",
			},
		},
		{
			"kind_name":       "miniprogram",
			"content_summary": "小程序标题",
			"message_content_parsed": map[string]any{
				"title":               "小程序标题",
				"url":                 "https://example.test/page",
				"source_display_name": "示例小程序",
			},
		},
		{
			"kind_name":       "transfer",
			"content_summary": "收到转账5.00元",
			"message_content_parsed": map[string]any{
				"des": "收到转账5.00元",
				"wcpayinfo": map[string]any{
					"feedesc":    "￥5.00",
					"pay_memo":   "周末聚餐 AA",
					"paysubtype": int64(1),
				},
			},
		},
		{
			"kind_name":       "red_packet",
			"content_summary": "恭喜发财",
			"message_content_parsed": map[string]any{
				"wcpayinfo": map[string]any{
					"sendertitle":   "恭喜发财",
					"scenetext":     "群红包",
					"receivertitle": "已领取",
					"nativeurl":     "wxpay://c2cbizmessagehandler/hongbao/receivehongbao",
				},
			},
		},
		{
			"kind_name":       "forward_chat",
			"content_summary": "聊天记录",
			"message_content_parsed": map[string]any{
				"title": "聊天记录",
				"des":   "2条",
				"forward_items": []any{
					map[string]any{"datatype": int64(1), "sourcename": "V", "datadesc": "hello"},
					map[string]any{"datatype": int64(8), "sourcename": "V", "datatitle": "report.pdf", "datasize": int64(123)},
				},
			},
		},
		{
			"kind_name":       "location",
			"content_summary": "[位置] Office",
			"message_content_parsed": map[string]any{
				"label":     "Office",
				"poiname":   "HQ",
				"latitude":  float64(31.2),
				"longitude": float64(121.5),
			},
		},
		{
			"kind_name":       "card",
			"content_summary": "[名片] A-返利小秘书",
			"message_content_parsed": map[string]any{
				"username":         "wxid_apxmfod6s63121",
				"nickname":         "A-返利小秘书",
				"alias":            "GJZ9990",
				"big_head_img_url": "https://wx.qlogo.cn/mmhead/ver_1/sample/0",
			},
		},
		{
			"kind_name":       "music",
			"content_summary": "神",
			"message_content_parsed": map[string]any{
				"app_subtype": int64(3),
				"title":       "神",
				"des":         "陈冠希",
				"url":         "http://c.y.qq.com/v8/playsong.html?songmid=002CVioa3fNGvD",
			},
		},
		{
			"kind_name":       "solitaire",
			"content_summary": "#接龙\n测试群接龙\n\n1. V\n2. 白日做梦",
			"message_content_parsed": map[string]any{
				"app_subtype": int64(53),
				"title":       "#接龙\n测试群接龙\n\n1. V\n2. 白日做梦",
			},
		},
	}

	got := agentMessages(rows)
	link := got[0]["link"].(map[string]any)
	if link["url"] != "https://mp.weixin.qq.com/s/q7WlpIORMaL1C7YATwK4zQ" || link["source"] != "AI寒武纪" {
		t.Fatalf("link payload = %#v", link)
	}
	mini := got[1]["miniprogram"].(map[string]any)
	if mini["title"] != "小程序标题" || mini["app"] != "示例小程序" {
		t.Fatalf("miniprogram payload = %#v", mini)
	}
	transfer := got[2]["transfer"].(map[string]any)
	if transfer["amount"] != "￥5.00" || transfer["memo"] != "周末聚餐 AA" {
		t.Fatalf("transfer payload = %#v", transfer)
	}
	redPacket := got[3]["red_packet"].(map[string]any)
	if redPacket["title"] != "恭喜发财" || redPacket["scene"] != "群红包" {
		t.Fatalf("red packet payload = %#v", redPacket)
	}
	forward := got[4]["forward_chat"].(map[string]any)
	if forward["item_count"] != 2 {
		t.Fatalf("forward payload = %#v", forward)
	}
	items, ok := forward["items"].([]map[string]any)
	if !ok || len(items) != 2 || items[0]["kind"] != "text" || items[1]["kind"] != "file" {
		t.Fatalf("forward preview = %#v", forward["items"])
	}
	loc := got[5]["location"].(map[string]any)
	if loc["label"] != "Office" || loc["name"] != "HQ" {
		t.Fatalf("location payload = %#v", loc)
	}
	card := got[6]["card"].(map[string]any)
	if card["username"] != "wxid_apxmfod6s63121" || card["display_name"] != "A-返利小秘书" || card["alias"] != "GJZ9990" || card["avatar_url"] != "https://wx.qlogo.cn/mmhead/ver_1/sample/0" {
		t.Fatalf("card payload = %#v", card)
	}
	music := got[7]["music"].(map[string]any)
	if music["title"] != "神" || music["artist"] != "陈冠希" || music["url"] != "http://c.y.qq.com/v8/playsong.html?songmid=002CVioa3fNGvD" {
		t.Fatalf("music payload = %#v", music)
	}
	solitaire := got[8]["solitaire"].(map[string]any)
	if solitaire["entry_count"] != 2 {
		t.Fatalf("solitaire payload = %#v", solitaire)
	}
	entries, ok := solitaire["entries"].([]string)
	if !ok || len(entries) != 2 || entries[0] != "V" || entries[1] != "白日做梦" {
		t.Fatalf("solitaire entries = %#v", solitaire["entries"])
	}
}

func TestAgentForwardChatRecursivelyExposesNestedPayloads(t *testing.T) {
	rows := []wcdb.Row{{
		"kind_name":       "forward_chat",
		"content_summary": "群聊的聊天记录",
		"media_read_hints": []map[string]any{{
			"source":                      "message_forward_item",
			"forward_path":                "0.0",
			"resource_family":             "image",
			"direct_readable_local_paths": []string{"/tmp/forward-image.jpg"},
		}},
		"message_content_parsed": map[string]any{
			"title": "群聊的聊天记录",
			"des":   "V: [聊天记录]\nV: 这个不错",
			"forward_items": []wxparse.ForwardItem{
				{
					DataType:         17,
					SourceName:       "V",
					SourceTime:       "2026-05-22 17:37:48",
					DataTitle:        "群聊的聊天记录",
					SrcMsgCreateTime: 1779442668,
					FromNewMsgID:     "745748254815614797",
					NestedItems: []wxparse.ForwardItem{
						{
							DataType:         2,
							SourceName:       "V",
							SourceTime:       "2026-05-22 16:44",
							DataFmt:          "jpg",
							FullMD5:          "12e8bd59b6f9b455803ae17225061090",
							DataSize:         12059,
							CDNDataURL:       "cdn-data",
							CDNDataKey:       "cdn-key",
							SrcMsgCreateTime: 1779439453,
							FromNewMsgID:     "2369251559671996886",
						},
						{
							DataType:         5,
							SourceName:       "V",
							SourceTime:       "2026-05-22 16:56",
							SrcMsgCreateTime: 1779440207,
							FromNewMsgID:     "6241033583148630707",
							Link: &wxparse.ForwardLink{
								URL:               "https://mp.weixin.qq.com/s/test",
								Title:             "一文搞懂如何在Codex中使用goals",
								SourceUsername:    "gh_7e5d9d010744",
								SourceDisplayName: "AI寒武纪",
								ThumbURL:          "https://example.test/thumb.jpg",
							},
						},
					},
				},
				{
					DataType:   1,
					SourceName: "V",
					DataDesc:   "这个不错",
					ReferMsg: &wxparse.ForwardReferMsg{
						Type:        48,
						SvrID:       "1841310816813489186",
						DisplayName: "V",
						Content:     `<msg><location x="27.452047" y="114.178642" label="山顶" poiname="金顶"/></msg>`,
						ReferDesc:   "金顶",
					},
				},
			},
		},
	}}

	got := agentMessages(rows)
	forward := got[0]["forward_chat"].(map[string]any)
	items := forward["items"].([]map[string]any)
	if len(items) != 2 || items[0]["kind"] != "forward_chat" {
		t.Fatalf("forward items = %#v", forward["items"])
	}
	nested := items[0]["items"].([]map[string]any)
	if len(nested) != 2 || nested[0]["kind"] != "image" || nested[1]["kind"] != "link" {
		t.Fatalf("nested items = %#v", items[0]["items"])
	}
	images := nested[0]["images"].([]map[string]any)
	if len(images) != 1 || images[0]["path"] != "/tmp/forward-image.jpg" || len(images[0]) != 1 {
		t.Fatalf("nested image = %#v", images[0])
	}
	sourceID := nested[0]["source_id"].(map[string]any)
	if sourceID["server_id_str"] != "2369251559671996886" {
		t.Fatalf("nested source_id = %#v", sourceID)
	}
	link := nested[1]["link"].(map[string]any)
	if link["url"] != "https://mp.weixin.qq.com/s/test" || link["source"] != "AI寒武纪" {
		t.Fatalf("nested link = %#v", link)
	}
	quote := items[1]["quote"].(map[string]any)
	loc := quote["location"].(map[string]any)
	if quote["kind"] != "location" || loc["name"] != "金顶" {
		t.Fatalf("forward item quote = %#v", quote)
	}
}

func TestAgentForwardChatWarnsWhenNestedMediaUnresolved(t *testing.T) {
	rows := []wcdb.Row{{
		"kind_name":       "forward_chat",
		"content_summary": "群聊的聊天记录",
		"message_content_parsed": map[string]any{
			"title": "群聊的聊天记录",
			"forward_items": []wxparse.ForwardItem{{
				DataType:     2,
				SourceName:   "V",
				FromNewMsgID: "2369251559671996886",
				FullMD5:      "12e8bd59b6f9b455803ae17225061090",
			}},
		},
	}}

	got := agentMessages(rows)
	forward := got[0]["forward_chat"].(map[string]any)
	items := forward["items"].([]map[string]any)
	warnings := stringSliceAny(items[0]["warnings"])
	if len(warnings) != 1 || warnings[0] != "forward_image_not_resolved" {
		t.Fatalf("forward item warnings = %#v", items[0]["warnings"])
	}
}

func TestAgentForwardChatWarnsWhenVoiceAndFileUnresolved(t *testing.T) {
	rows := []wcdb.Row{{
		"kind_name":       "forward_chat",
		"content_summary": "群聊的聊天记录",
		"message_content_parsed": map[string]any{
			"title": "群聊的聊天记录",
			"forward_items": []wxparse.ForwardItem{
				{DataType: 3, SourceName: "V", DataTitle: "语音", DataFmt: "silk"},
				{DataType: 8, SourceName: "V", DataTitle: "report.pdf", DataFmt: "pdf", DataSize: 123},
			},
		},
	}}

	got := agentMessages(rows)
	forward := got[0]["forward_chat"].(map[string]any)
	items := forward["items"].([]map[string]any)
	voiceWarnings := stringSliceAny(items[0]["warnings"])
	if len(voiceWarnings) != 1 || voiceWarnings[0] != "forward_voice_not_resolved" {
		t.Fatalf("voice warnings = %#v; item=%#v", items[0]["warnings"], items[0])
	}
	fileWarnings := stringSliceAny(items[1]["warnings"])
	if len(fileWarnings) != 1 || fileWarnings[0] != "forward_file_not_resolved" {
		t.Fatalf("file warnings = %#v; item=%#v", items[1]["warnings"], items[1])
	}
}

func TestForwardResourceReadHintsResolveImageBySourceServerID(t *testing.T) {
	root := t.TempDir()
	srv := &server{cfg: &config.Config{DBRoot: root}}
	sourceTalker := "source@chatroom"
	serverID := int64(2369251559671996886)
	ts := time.Date(2026, 5, 22, 16, 44, 13, 0, time.Local).Unix()
	imageBytes := tinyPNG()
	resourceMD5 := "cb2f09a6a1eba3b08ab1edbf2a209052"
	direct := filepath.Join(root, "temp", "InputTemp", "forward.png")
	if err := os.MkdirAll(filepath.Dir(direct), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(direct, imageBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	dat := filepath.Join(root, "msg", "attach", talkerHash(sourceTalker), "2026-05", "Img", resourceMD5+".dat")
	if err := os.MkdirAll(filepath.Dir(dat), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dat, []byte("encrypted"), 0o644); err != nil {
		t.Fatal(err)
	}
	resources := []map[string]any{{
		"source_server_id":  serverID,
		"resource_family":   "image",
		"resource_id":       int64(269107),
		"resource_type_raw": int64(65537),
		"local_paths":       srv.localMediaPaths(sourceTalker, ts, "image", resourceMD5, nil),
	}}
	contentSum := md5.Sum(imageBytes)
	srv.attachReadableImageMatchesByContentMD5(resources[0], ts, hex.EncodeToString(contentSum[:]))

	hintsByPath := forwardHintsFromResources(resources, map[int64][]string{serverID: {"0"}})
	hints := hintsByPath["0"]
	if len(hints) != 1 {
		t.Fatalf("forward resource hints = %#v, want one hint", hintsByPath)
	}
	if hints[0]["source"] != "message_forward_item" || hints[0]["forward_path"] != "0" || hints[0]["source_server_id_str"] != strconv.FormatInt(serverID, 10) {
		t.Fatalf("forward hint metadata = %#v", hints[0])
	}
	directPaths := stringSliceAny(hints[0]["direct_readable_local_paths"])
	if len(directPaths) != 1 || directPaths[0] != direct {
		t.Fatalf("direct paths = %#v, want [%q]", directPaths, direct)
	}
	refs := forwardMediaRefsFromHints([]int{0}, hints, "image")
	if len(refs) != 1 || refs[0]["path"] != direct {
		t.Fatalf("forward media refs = %#v, want direct path", refs)
	}
}

func TestAgentImageRefsPreferOriginalOverThumbnail(t *testing.T) {
	row := wcdb.Row{
		"kind_name": "image",
		"media_read_hints": []map[string]any{{
			"source":                      "message_resource",
			"address_type":                "local_file",
			"resource_family":             "image",
			"direct_readable_local_paths": []string{"/tmp/sample_t.jpg", "/tmp/sample_h.png"},
			"local_path_details": []map[string]any{{
				"path":      "/tmp/sample_t.jpg",
				"file_size": int64(1000),
				"width":     180,
				"height":    102,
			}, {
				"path":      "/tmp/sample_h.png",
				"file_size": int64(100),
				"width":     790,
				"height":    202,
			}},
		}},
	}

	images, warnings := agentImageRefs(row, false)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	if len(images) != 1 || images[0]["path"] != "/tmp/sample_h.png" {
		t.Fatalf("images = %#v, want high/original path only", images)
	}
}

func TestAgentImageRefsFallsBackToThumbnailWhenOnlyThumbnailExists(t *testing.T) {
	row := wcdb.Row{
		"kind_name": "image",
		"media_read_hints": []map[string]any{{
			"source":                      "message_resource",
			"address_type":                "local_file",
			"resource_family":             "image",
			"direct_readable_local_paths": []string{"/tmp/sample_t.jpg"},
			"local_path_details": []map[string]any{{
				"path":   "/tmp/sample_t.jpg",
				"width":  180,
				"height": 102,
			}},
		}},
	}

	images, warnings := agentImageRefs(row, false)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	if len(images) != 1 || images[0]["path"] != "/tmp/sample_t.jpg" {
		t.Fatalf("images = %#v, want thumbnail fallback", images)
	}
}

func TestForwardImageRefsPreferOriginalOverThumbnail(t *testing.T) {
	hints := []map[string]any{{
		"source":                      "message_forward_item",
		"forward_path":                "0",
		"resource_family":             "image",
		"direct_readable_local_paths": []string{"/tmp/forward_t.jpg", "/tmp/forward.png"},
		"local_path_details": []map[string]any{{
			"path":   "/tmp/forward_t.jpg",
			"width":  180,
			"height": 102,
		}, {
			"path":   "/tmp/forward.png",
			"width":  790,
			"height": 202,
		}},
	}}

	refs := forwardMediaRefsFromHints([]int{0}, hints, "image")
	if len(refs) != 1 || refs[0]["path"] != "/tmp/forward.png" {
		t.Fatalf("forward refs = %#v, want original/high path only", refs)
	}
}

func TestAgentForwardChatSourceReuseDoesNotDuplicateNestedForward(t *testing.T) {
	sourceForward := wcdb.Row{
		"talker":        "room@chatroom",
		"local_id":      int64(3),
		"server_id_str": "745748254815614797",
		"kind_name":     "forward_chat",
		"message_content_parsed": map[string]any{
			"title": "群聊的聊天记录",
			"forward_items": []wxparse.ForwardItem{{
				DataType:   1,
				SourceName: "V",
				DataDesc:   "hello",
			}},
		},
	}
	wrapper := wcdb.Row{
		"talker":        "room@chatroom",
		"local_id":      int64(30),
		"server_id_str": "5634630697991999050",
		"kind_name":     "forward_chat",
		"message_content_parsed": map[string]any{
			"title": "群聊的聊天记录",
			"forward_items": []wxparse.ForwardItem{{
				DataType:     17,
				SourceName:   "V",
				DataTitle:    "群聊的聊天记录",
				FromNewMsgID: "745748254815614797",
				NestedItems: []wxparse.ForwardItem{{
					DataType:   1,
					SourceName: "V",
					DataDesc:   "hello",
				}},
			}},
		},
	}

	got := agentMessages([]wcdb.Row{sourceForward, wrapper})
	forward := got[1]["forward_chat"].(map[string]any)
	items := forward["items"].([]map[string]any)
	if _, ok := items[0]["forward_chat"]; ok {
		t.Fatalf("nested forward item duplicated source forward_chat payload: %#v", items[0])
	}
	if nested := items[0]["items"].([]map[string]any); len(nested) != 1 || nested[0]["text"] != "hello" {
		t.Fatalf("nested items = %#v", items[0]["items"])
	}
}

func TestAgentQuoteStructuresReferencedFileAndMusic(t *testing.T) {
	rows := []wcdb.Row{
		{
			"kind_name":           "quote",
			"content_summary":     "[引用: report.pdf] 值得学习",
			"sender_wxid":         "wxid_reply",
			"sender_display_name": "Reply Sender",
			"message_content_parsed": map[string]any{
				"title": "值得学习",
				"refermsg": map[string]any{
					"type":        int64(49),
					"displayname": "V",
					"content_raw": "<msg><appmsg><title>report.pdf</title><type>6</type></appmsg></msg>",
					"content_parsed": map[string]any{
						"app_subtype": int64(6),
						"title":       "report.pdf",
						"app_attach": map[string]any{
							"total_len": int64(123),
							"file_ext":  "pdf",
						},
					},
				},
			},
		},
		{
			"kind_name":           "quote",
			"content_summary":     "[引用: 神] 这歌很好听",
			"sender_wxid":         "wxid_reply",
			"sender_display_name": "Reply Sender",
			"message_content_parsed": map[string]any{
				"title": "这歌很好听",
				"refermsg": map[string]any{
					"type":        int64(49),
					"displayname": "V",
					"content_raw": "<msg><appmsg><title>神</title><des>陈冠希</des><type>76</type></appmsg></msg>",
					"content_parsed": map[string]any{
						"app_subtype": int64(76),
						"title":       "神",
						"des":         "陈冠希",
						"url":         "http://c.y.qq.com/v8/playsong.html?songmid=002CVioa3fNGvD",
					},
				},
			},
		},
	}

	got := agentMessages(rows)
	fileQuote := got[0]["quote"].(map[string]any)
	if fileQuote["kind"] != "file" {
		t.Fatalf("file quote kind = %#v", fileQuote)
	}
	files, ok := fileQuote["files"].([]map[string]any)
	if !ok || len(files) != 1 || files[0]["name"] != "report.pdf" || files[0]["file_size"] != int64(123) {
		t.Fatalf("file quote files = %#v", fileQuote["files"])
	}
	musicQuote := got[1]["quote"].(map[string]any)
	if musicQuote["kind"] != "music" {
		t.Fatalf("music quote kind = %#v", musicQuote)
	}
	music := musicQuote["music"].(map[string]any)
	if music["title"] != "神" || music["artist"] != "陈冠希" {
		t.Fatalf("music quote payload = %#v", music)
	}
}

func TestChatTimelineEnvelopeIncludesQueryAndFreshness(t *testing.T) {
	rows := []wcdb.Row{
		{"talker": "room@chatroom", "talker_display_name": "AI Agent", "local_id": int64(1), "create_time": int64(100), "kind_name": "text", "content_summary": "old"},
		{"talker": "room@chatroom", "talker_display_name": "AI Agent", "local_id": int64(2), "create_time": int64(200), "kind_name": "text", "content_summary": "new"},
	}
	msgs := agentMessages(rows)
	env := messageTimelineEnvelope(
		map[string]any{"chat": "AI Agent", "limit": 2, "order": "desc"},
		rows,
		msgs,
		messagePageInfo{Limit: 2, Offset: 0, Returned: 2, HasMore: true, NextOffset: 2},
		"sort_seq DESC, local_id DESC",
		"asc",
	)
	if _, ok := env["messages"].([]map[string]any); !ok {
		t.Fatalf("messages = %#v, want agent message rows", env["messages"])
	}
	query := env["query"].(map[string]any)
	if query["chat"] != "AI Agent" || query["talker"] != "room@chatroom" || query["display_order"] != "asc" || query["returned"] != 2 || query["has_more"] != true || query["next_offset"] != 2 {
		t.Fatalf("query meta = %#v", query)
	}
	cursor := query["cursor"].(map[string]any)
	if cursor["oldest_local_id"] != int64(1) || cursor["newest_local_id"] != int64(2) || cursor["next_after_message"] != int64(2) {
		t.Fatalf("cursor meta = %#v", cursor)
	}
	freshness := env["freshness"].(map[string]any)
	if freshness["message_source"] != "live_message_db" || freshness["metadata_cache_role"] != "name_resolution_only" || freshness["last_message_time"] != time.Unix(200, 0).Format("2006-01-02 15:04:05") {
		t.Fatalf("freshness meta = %#v", freshness)
	}
}

func TestReadEventsCursorHelpers(t *testing.T) {
	args := map[string]any{"cursor": "local_id:42"}
	applyReadEventsCursor(args, "local_id:42")
	if args["after_message"] != int64(42) {
		t.Fatalf("after_message = %#v, want 42", args["after_message"])
	}
	events := []map[string]any{{"cursor": "local_id:1"}, {"cursor": "local_id:2"}}
	if got := newestReadEventsCursor(events); got != "local_id:2" {
		t.Fatalf("newestReadEventsCursor = %q", got)
	}
}

func TestAmbiguousChatCandidates(t *testing.T) {
	if !ambiguousChatCandidates([]chatCandidate{
		{Username: "a@chatroom", DisplayName: "AI Agent A", Match: "display_name_fuzzy"},
		{Username: "b@chatroom", DisplayName: "AI Agent B", Match: "display_name_fuzzy"},
	}) {
		t.Fatalf("multiple fuzzy candidates should be ambiguous")
	}
	if !ambiguousChatCandidates([]chatCandidate{
		{Username: "a@chatroom", DisplayName: "AI Agent", Match: "display_name_exact"},
		{Username: "b@chatroom", DisplayName: "AI Agent", Match: "remark_exact"},
	}) {
		t.Fatalf("multiple exact candidates should be ambiguous")
	}
	if ambiguousChatCandidates([]chatCandidate{
		{Username: "a@chatroom", DisplayName: "AI Agent", Match: "display_name_exact"},
		{Username: "b@chatroom", DisplayName: "AI Agent Archive", Match: "display_name_fuzzy"},
	}) {
		t.Fatalf("one exact candidate plus weaker fuzzy candidate should resolve")
	}
	if ambiguousChatCandidates([]chatCandidate{{Username: "a@chatroom", DisplayName: "AI Agent A", Match: "display_name_fuzzy"}}) {
		t.Fatalf("single fuzzy candidate should not be ambiguous")
	}
}

func TestSortLiveMessageRowsAcrossShards(t *testing.T) {
	rows := []wcdb.Row{
		{"local_id": int64(1), "sort_seq": int64(100), "create_time": int64(30)},
		{"local_id": int64(3), "sort_seq": int64(300), "create_time": int64(10)},
		{"local_id": int64(2), "sort_seq": int64(200), "create_time": int64(20)},
	}
	sortLiveMessageRows(rows, "sort_seq DESC, local_id DESC")
	if got := []int64{rowInt64(rows[0], "local_id"), rowInt64(rows[1], "local_id"), rowInt64(rows[2], "local_id")}; got[0] != 3 || got[1] != 2 || got[2] != 1 {
		t.Fatalf("sort_seq desc order = %v, want [3 2 1]", got)
	}
	sortLiveMessageRows(rows, "create_time ASC, local_id ASC")
	if got := []int64{rowInt64(rows[0], "local_id"), rowInt64(rows[1], "local_id"), rowInt64(rows[2], "local_id")}; got[0] != 3 || got[1] != 2 || got[2] != 1 {
		t.Fatalf("create_time asc order = %v, want [3 2 1]", got)
	}
}

func TestMessageOrderAndDisplayOrder(t *testing.T) {
	sql, err := messageQueryOrderSQL(map[string]any{"order": "asc"})
	if err != nil || sql != "sort_seq ASC, local_id ASC" {
		t.Fatalf("messageQueryOrderSQL asc = (%q,%v)", sql, err)
	}
	sql, err = messageQueryOrderSQL(map[string]any{})
	if err != nil || sql != "sort_seq DESC, local_id DESC" {
		t.Fatalf("messageQueryOrderSQL default = (%q,%v)", sql, err)
	}
	if display, err := messagesDisplayOrder(map[string]any{"display_order": "asc"}); err != nil || display != "asc" {
		t.Fatalf("messagesDisplayOrder asc = (%q,%v)", display, err)
	}
	rows := []wcdb.Row{
		{"local_id": int64(3), "create_time": int64(30)},
		{"local_id": int64(1), "create_time": int64(10)},
		{"local_id": int64(2), "create_time": int64(20)},
	}
	applyMessageDisplayOrder(rows, "asc")
	if got := []int64{rowInt64(rows[0], "local_id"), rowInt64(rows[1], "local_id"), rowInt64(rows[2], "local_id")}; got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("display_order asc = %v, want [1 2 3]", got)
	}
}

func TestSliceRowsAppliesGlobalOffsetAndLimit(t *testing.T) {
	rows := []wcdb.Row{
		{"local_id": int64(1)},
		{"local_id": int64(2)},
		{"local_id": int64(3)},
		{"local_id": int64(4)},
	}
	got := sliceRows(rows, 1, 2)
	if len(got) != 2 || rowInt64(got[0], "local_id") != 2 || rowInt64(got[1], "local_id") != 3 {
		t.Fatalf("sliceRows offset/limit = %#v, want local_id [2 3]", got)
	}
	if got := sliceRows(rows, 10, 2); got != nil {
		t.Fatalf("sliceRows past end = %#v, want nil", got)
	}
}

func TestValidateToolArgsRejectsBadInteger(t *testing.T) {
	if err := validateToolArgs("sessions", map[string]any{"limit": "bad"}); err == nil {
		t.Fatalf("validateToolArgs should reject string limit")
	}
	if err := validateToolArgs("sessions", map[string]any{"limit": 5001}); err == nil {
		t.Fatalf("validateToolArgs should reject oversized limit")
	}
	if err := validateToolArgs("sessions", map[string]any{"limit": 50}); err != nil {
		t.Fatalf("validateToolArgs rejected valid limit: %v", err)
	}
}

func TestValidateToolArgsRequired(t *testing.T) {
	if err := validateToolArgs("search", map[string]any{}); err == nil {
		t.Fatalf("validateToolArgs should reject missing required keyword")
	}
}

func TestValidateToolArgsRejectsUnknownAndBadEnums(t *testing.T) {
	if err := validateToolArgs("sessions", map[string]any{"bogus": "x"}); err == nil {
		t.Fatalf("validateToolArgs should reject unknown args")
	}
	if err := validateToolArgs("messages", map[string]any{"fields": "raw"}); err == nil {
		t.Fatalf("validateToolArgs should reject bad fields enum")
	}
	if err := validateToolArgs("messages", map[string]any{"include_media_paths": true}); err != nil {
		t.Fatalf("validateToolArgs should allow include_media_paths for messages: %v", err)
	}
	if err := validateToolArgs("messages", map[string]any{"view": "agent"}); err != nil {
		t.Fatalf("validateToolArgs should allow view=agent for messages: %v", err)
	}
	if err := validateToolArgs("messages", map[string]any{"view": "debug"}); err == nil {
		t.Fatalf("validateToolArgs should reject bad messages view enum")
	}
	if err := validateToolArgs("messages", map[string]any{"order": "desc", "display_order": "asc", "include_debug": true}); err != nil {
		t.Fatalf("validateToolArgs should allow order/display_order/include_debug for messages: %v", err)
	}
	if err := validateToolArgs("messages", map[string]any{"display_order": "sideways"}); err == nil {
		t.Fatalf("validateToolArgs should reject bad display_order enum")
	}
	if err := validateToolArgs("chat_timeline", map[string]any{"chat": "AI Agent", "limit": 10, "include_images": true}); err != nil {
		t.Fatalf("validateToolArgs should allow chat_timeline agent arguments: %v", err)
	}
	if err := validateToolArgs("export_messages", map[string]any{"path": "/tmp/x", "format": "csv"}); err == nil {
		t.Fatalf("validateToolArgs should reject bad format enum")
	}
	if err := validateToolArgs("sessions", map[string]any{"type_filter": "private,group"}); err != nil {
		t.Fatalf("validateToolArgs rejected comma type_filter: %v", err)
	}
	if err := validateToolArgs("sessions", map[string]any{"type_filter": "nope"}); err == nil {
		t.Fatalf("validateToolArgs should reject unknown type_filter")
	}
	if err := validateToolArgs("resolve_chat", map[string]any{"chat": "张三"}); err != nil {
		t.Fatalf("validateToolArgs should allow resolve_chat aliases: %v", err)
	}
	if err := validateToolArgs("media_resources", map[string]any{"server_id_str": "7710666891970547832"}); err != nil {
		t.Fatalf("validateToolArgs should allow string server id for media_resources: %v", err)
	}
}

func TestConfigReadyRequiresSchema2Keys(t *testing.T) {
	if (&config.Config{Key: strings.Repeat("a", 64)}).Ready() {
		t.Fatalf("legacy master key must not be treated as ready")
	}
	if !(&config.Config{Keys: map[string]string{"salt": "enc"}}).Ready() {
		t.Fatalf("schema-2 key map should be ready")
	}
}

func TestKeyRefreshReasonKeyUsesMissingSalt(t *testing.T) {
	got := keyRefreshReasonKey("no enc_key for salt 0123456789abcdef0123456789abcdef in /tmp/message.db - refresh wxkey's schema-2 key map")
	if got != "salt:0123456789abcdef0123456789abcdef" {
		t.Fatalf("keyRefreshReasonKey = %q", got)
	}
}

func TestRefreshReasonAlreadySatisfiedReloadsMissingSalt(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	salt := "0123456789abcdef0123456789abcdef"
	cfgBytes, err := json.Marshal(config.Config{
		Wxid:   "wxid_test",
		DBRoot: dir,
		Keys:   map[string]string{salt: "enc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WX_MCP_CONFIG", cfgPath)
	srv := &server{}
	reason := "no enc_key for salt " + salt + " in message/message_0.db"
	if !srv.refreshReasonAlreadySatisfied(reason) {
		t.Fatalf("missing salt should be satisfied after config reload")
	}
	if srv.cfg == nil || srv.cfg.Keys[salt] != "enc" || !srv.ok {
		t.Fatalf("server config was not refreshed: %#v ok=%v", srv.cfg, srv.ok)
	}
}

func TestMergeRuntimeKeyConfigKeepsExistingKeys(t *testing.T) {
	xorKey := 240
	oldCfg := &config.Config{
		Wxid:        "wxid_old",
		DBRoot:      "/old",
		Keys:        map[string]string{"old_salt": "old_key", "shared": "old_shared"},
		ImageKey:    "image_key",
		ImageXORKey: &xorKey,
		KeyEpoch:    20,
	}
	fresh := &config.Config{
		Wxid:     "wxid_new",
		DBRoot:   "/new",
		Keys:     map[string]string{"shared": "new_shared", "new_salt": "new_key"},
		KeyEpoch: 10,
	}

	got := mergeRuntimeKeyConfig(oldCfg, fresh)
	if got.Keys["old_salt"] != "old_key" || got.Keys["new_salt"] != "new_key" || got.Keys["shared"] != "new_shared" {
		t.Fatalf("merged keys = %#v", got.Keys)
	}
	if got.ImageKey != "image_key" || got.ImageXORKey == nil || *got.ImageXORKey != xorKey {
		t.Fatalf("image key fields were not preserved: %#v", got)
	}
	if got.Wxid != "wxid_new" || got.DBRoot != "/new" {
		t.Fatalf("fresh identity should win: %#v", got)
	}
	if got.KeyEpoch != 20 {
		t.Fatalf("KeyEpoch = %d, want 20", got.KeyEpoch)
	}
}

func TestCriticalCacheSourceClassification(t *testing.T) {
	for _, rel := range []string{"contact/contact.db", "session/session.db"} {
		if !isCriticalCacheSource(rel) {
			t.Fatalf("%s should be critical", rel)
		}
	}
	for _, rel := range []string{"message/message_0.db", "message/biz_message_1.db", "message/message_resource.db", "message/message_fts.db", "migrate/unspportmsg.db", "favorite/favorite.db"} {
		if isCriticalCacheSource(rel) {
			t.Fatalf("%s should not be critical", rel)
		}
	}
}

func TestStaleCacheIndexUsable(t *testing.T) {
	for _, reason := range []string{
		"changed source db: session/session.db",
		"critical snapshot error: session/session.db",
	} {
		if !staleCacheIndexUsable(reason) {
			t.Fatalf("%q should allow serving the existing metadata index", reason)
		}
	}
	if staleCacheIndexUsable("cache index missing") {
		t.Fatalf("missing index should not be treated as usable")
	}
	if !cacheRefreshWouldLikelyRepeat("critical snapshot error: session/session.db") {
		t.Fatalf("critical snapshot errors should not spawn repeated background refreshes")
	}
}

func TestStrictReadOnlyRejectsLocalWriteTools(t *testing.T) {
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
	srv := &server{}
	for name, fn := range map[string]func() (any, error){
		"cache_refresh": func() (any, error) { return srv.toolCacheRefresh(nil) },
		"cache_rebuild": func() (any, error) { return srv.toolCacheRebuild(nil) },
		"export_messages": func() (any, error) {
			return srv.toolExportMessages(map[string]any{
				"chat": "wxid_a",
				"path": filepath.Join(t.TempDir(), "chat.jsonl"),
			})
		},
	} {
		if _, err := fn(); err == nil || !strings.Contains(err.Error(), "strict_read_only") {
			t.Fatalf("%s error = %v, want strict_read_only", name, err)
		}
	}
}

func TestMetadataStatusReason(t *testing.T) {
	tests := []struct {
		reason string
		want   string
	}{
		{
			reason: "changed source db: contact/contact.db",
			want:   "metadata source changed since last snapshot: contact/contact.db",
		},
		{
			reason: "new source db: session/session.db",
			want:   "new metadata source detected: session/session.db",
		},
		{
			reason: "snapshot missing: contact/contact.db",
			want:   "metadata snapshot missing: contact/contact.db",
		},
		{
			reason: "critical snapshot error: session/session.db",
			want:   "metadata snapshot error: session/session.db",
		},
		{
			reason: "legacy message cache present; rebuild metadata cache",
			want:   "legacy message cache present; rebuild metadata cache",
		},
		{
			reason: "changed source db: message/message_0.db",
			want:   "changed source db: message/message_0.db",
		},
	}
	for _, tt := range tests {
		if got := metadataStatusReason(tt.reason); got != tt.want {
			t.Fatalf("metadataStatusReason(%q) = %q, want %q", tt.reason, got, tt.want)
		}
	}
}

func TestResolveChatServesDegradedMetadataCache(t *testing.T) {
	wcdbPath, err := filepath.Abs(filepath.Join("..", "..", "lib", "libWCDB.dylib"))
	if err != nil {
		t.Fatalf("abs wcdb path: %v", err)
	}
	if _, err := os.Stat(wcdbPath); err != nil {
		t.Skipf("libWCDB.dylib not available: %v", err)
	}
	t.Setenv("WECHAT_CLI_WCDB_DYLIB", wcdbPath)
	t.Setenv("WECHAT_CLI_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("WECHAT_CLI_DISABLE_AUTO_REFRESH", "1")

	dbRoot := filepath.Join(t.TempDir(), "wechat-root")
	for _, rel := range []string{"contact/contact.db", "session/session.db"} {
		path := filepath.Join(dbRoot, "db_storage", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir source: %v", err)
		}
		if err := os.WriteFile(path, []byte("0123456789abcdef"), 0o600); err != nil {
			t.Fatalf("write source: %v", err)
		}
	}
	cfg := &config.Config{
		Wxid:   "wxid_degraded_test",
		DBRoot: dbRoot,
		Keys:   map[string]string{"dummy": "dummy"},
	}
	paths, err := cachePathsFor(cfg)
	if err != nil {
		t.Fatalf("cache paths: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.IndexPath), 0o700); err != nil {
		t.Fatalf("mkdir index: %v", err)
	}
	if err := wcdb.Bootstrap(wcdbPath); err != nil {
		t.Fatalf("bootstrap wcdb: %v", err)
	}
	db, err := wcdb.OpenPlain(paths.IndexPath, true)
	if err != nil {
		t.Fatalf("open test index: %v", err)
	}
	if err := createIndexSchema(db); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	contactSrc := filepath.Join(dbRoot, "db_storage", "contact", "contact.db")
	sessionSrc := filepath.Join(dbRoot, "db_storage", "session", "session.db")
	contactSnapshot := filepath.Join(paths.RawDir, "contact", "contact.db")
	if err := os.MkdirAll(filepath.Dir(contactSnapshot), 0o700); err != nil {
		t.Fatalf("mkdir contact snapshot: %v", err)
	}
	if err := os.WriteFile(contactSnapshot, []byte("snapshot"), 0o600); err != nil {
		t.Fatalf("write contact snapshot: %v", err)
	}
	if err := db.ExecArgs(`INSERT INTO cache_files
		(rel_path, source_path, snapshot_path, db_mtime, wal_mtime, salt_hex, status, error, copied_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"contact/contact.db", contactSrc, contactSnapshot,
		fileMTimeNanos(contactSrc), fileMTimeNanos(contactSrc+"-wal"), readSaltHex(contactSrc),
		"ok", "", time.Now().Unix()); err != nil {
		t.Fatalf("insert contact meta: %v", err)
	}
	if err := db.ExecArgs(`INSERT INTO cache_files
		(rel_path, source_path, snapshot_path, db_mtime, wal_mtime, salt_hex, status, error, copied_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"session/session.db", sessionSrc, filepath.Join(paths.RawDir, "session", "session.db"),
		fileMTimeNanos(sessionSrc), fileMTimeNanos(sessionSrc+"-wal"), readSaltHex(sessionSrc),
		"error", "no enc_key for salt test", time.Now().Unix()); err != nil {
		t.Fatalf("insert session meta: %v", err)
	}
	if err := db.ExecArgs(`INSERT INTO contacts_unified
		(username, display_name, nick_name, remark, alias, description, type, is_verified)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"room@chatroom", "wxmcp测试群", "wxmcp测试群", "", "", "", "group", 0); err != nil {
		t.Fatalf("insert contact: %v", err)
	}
	db.Close()

	srv := &server{cfg: cfg, wcdbPath: wcdbPath, ok: true}
	got, err := srv.toolResolveChat(map[string]any{"query": "wxmcp测试群"})
	if err != nil {
		t.Fatalf("toolResolveChat returned error: %v", err)
	}
	out := got.(map[string]any)
	warnings, ok := out["warnings"].([]string)
	if !ok || len(warnings) == 0 || !strings.Contains(warnings[0], "session/session.db") {
		t.Fatalf("warnings = %#v, want degraded session metadata warning", out["warnings"])
	}
	cands := out["candidates"].([]chatCandidate)
	if len(cands) != 1 || cands[0].Username != "room@chatroom" {
		t.Fatalf("candidates = %#v", cands)
	}
}

func TestParseKVFlagsPreservesStringIDs(t *testing.T) {
	flags := parseKVFlags([]string{"--server-id-str", "7710666891970547832", "--limit", "1"})
	if got, ok := flags["server_id_str"].(string); !ok || got != "7710666891970547832" {
		t.Fatalf("server_id_str = %#v, want string", flags["server_id_str"])
	}
	if got, ok := flags["limit"].(int64); !ok || got != 1 {
		t.Fatalf("limit = %#v, want int64(1)", flags["limit"])
	}
}

func TestParseSnsXMLMediaMetadata(t *testing.T) {
	raw := `<SnsDataItem><TimelineObject><id>tid1</id><username>wxid_a</username><createTime>1776330000</createTime><contentDesc>hello</contentDesc><ContentObject><type>1</type><mediaList><media><type>15</type><sub_type>10</sub_type><url enc_idx="1" key="video-key" token="video-token">https://example.test/video.mp4</url><thumb enc_idx="0" key="thumb-key" token="thumb-token">https://example.test/thumb.jpg</thumb><size width="720" height="1280" totalSize="123456" /><videomd5>video-md5</videomd5><videoDuration>37</videoDuration></media></mediaList></ContentObject></TimelineObject><LocalExtraInfo><nickname>Alice</nickname></LocalExtraInfo></SnsDataItem>`
	post, err := parseSnsXML(raw)
	if err != nil {
		t.Fatalf("parseSnsXML returned error: %v", err)
	}
	if len(post.Media) != 1 {
		t.Fatalf("media len = %d, want 1", len(post.Media))
	}
	m := post.Media[0]
	if m.Type != "video" || m.RawType != 15 || m.SubType != "10" {
		t.Fatalf("media type fields = %#v", m)
	}
	if m.URLKey != "video-key" || m.URLToken != "video-token" || m.URLEncIdx != "1" {
		t.Fatalf("url metadata missing: %#v", m)
	}
	if m.ThumbKey != "thumb-key" || m.ThumbToken != "thumb-token" || m.ThumbEncIdx != "0" {
		t.Fatalf("thumb metadata missing: %#v", m)
	}
	if m.Width != 720 || m.Height != 1280 || m.TotalSize != 123456 || m.VideoMD5 != "video-md5" || m.VideoDuration != 37 {
		t.Fatalf("size/video metadata missing: %#v", m)
	}
}

func TestPackedStringsExtractsResourceNamesAndMD5(t *testing.T) {
	fileBlob, err := hex.DecodeString("0A310A1571626974746F7272656E742D352E302E352E646D67121871626974746F7272656E742D352E302E352831292E646D67")
	if err != nil {
		t.Fatal(err)
	}
	got := packedStrings(fileBlob)
	if len(got) != 2 || got[0] != "qbittorrent-5.0.5.dmg" || got[1] != "qbittorrent-5.0.5(1).dmg" {
		t.Fatalf("packedStrings(file) = %#v", got)
	}
	md5Blob, err := hex.DecodeString("12220A206665383737363333396364363765363032336437653437623937623037336130")
	if err != nil {
		t.Fatal(err)
	}
	got = packedStrings(md5Blob)
	if len(got) != 1 || got[0] != "fe8776339cd67e6023d7e47b97b073a0" {
		t.Fatalf("packedStrings(md5) = %#v", got)
	}
}

func TestLocalMediaPathsUsesExactWeChatLayout(t *testing.T) {
	root := t.TempDir()
	srv := &server{cfg: &config.Config{DBRoot: root}}
	talker := "wxid_media_test"
	ts := time.Date(2026, 5, 9, 12, 0, 0, 0, time.Local).Unix()
	md5 := "fe8776339cd67e6023d7e47b97b073a0"
	img := filepath.Join(root, "msg", "attach", talkerHash(talker), "2026-05", "Img", md5+".dat")
	if err := os.MkdirAll(filepath.Dir(img), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(img, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := srv.localMediaPaths(talker, ts, "image", md5, nil)
	if len(paths) != 1 || paths[0] != img {
		t.Fatalf("image localMediaPaths = %#v, want %q", paths, img)
	}

	fileName := "report.pdf"
	filePath := filepath.Join(root, "msg", "file", "2026-05", fileName)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths = srv.localMediaPaths(talker, ts, "file", "", []string{fileName, "../bad.pdf"})
	if len(paths) != 1 || paths[0] != filePath {
		t.Fatalf("file localMediaPaths = %#v, want %q", paths, filePath)
	}
}

func TestLocalMediaPathsPrefersDirectReadableTempImageByMD5(t *testing.T) {
	root := t.TempDir()
	srv := &server{cfg: &config.Config{DBRoot: root}}
	talker := "wxid_media_test"
	ts := time.Date(2026, 5, 9, 12, 0, 0, 0, time.Local).Unix()
	imageBytes := tinyPNG()
	sum := md5.Sum(imageBytes)
	md5Value := hex.EncodeToString(sum[:])

	direct := filepath.Join(root, "temp", "InputTemp", "direct.png")
	if err := os.MkdirAll(filepath.Dir(direct), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(direct, imageBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	imgDAT := filepath.Join(root, "msg", "attach", talkerHash(talker), "2026-05", "Img", md5Value+".dat")
	if err := os.MkdirAll(filepath.Dir(imgDAT), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imgDAT, []byte("encrypted"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths := srv.localMediaPaths(talker, ts, "image", md5Value, nil)
	if len(paths) != 2 || paths[0] != direct || paths[1] != imgDAT {
		t.Fatalf("image localMediaPaths = %#v, want direct PNG then .dat", paths)
	}
}

func TestDecodeLocalImageForAgentWritesDecodedPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	key := []byte("abcdefghijklmnop")
	img := filepath.Join(root, "msg", "attach", "hash", "2026-05", "Img", "sample.dat")
	if err := os.MkdirAll(filepath.Dir(img), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(img, testWechatV4ImageDAT(t, key, tinyPNG()), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &server{cfg: &config.Config{Wxid: "wxid_test", DBRoot: root, ImageKey: hex.EncodeToString(key)}}
	res := map[string]any{}
	srv.attachLocalPathAgentFields(res, "image", []string{img})

	if direct, ok := res["direct_readable"].(bool); !ok || !direct {
		t.Fatalf("direct_readable = %#v, want true after decode", res["direct_readable"])
	}
	decodedPaths, ok := res["decoded_local_paths"].([]string)
	if !ok || len(decodedPaths) != 1 {
		t.Fatalf("decoded_local_paths = %#v, want one decoded path", res["decoded_local_paths"])
	}
	if _, err := os.Stat(decodedPaths[0]); err != nil {
		t.Fatalf("decoded path is not readable: %v", err)
	}
	if !strings.HasPrefix(decodedPaths[0], filepath.Join(home, ".wechat-cli", "media-cache", "wxid_test")) {
		t.Fatalf("decoded path %q not under media cache", decodedPaths[0])
	}
	details, ok := res["local_path_details"].([]map[string]any)
	if !ok || len(details) != 1 {
		t.Fatalf("local_path_details = %#v, want one detail", res["local_path_details"])
	}
	d := details[0]
	if d["decode_status"] != "decoded" || d["decoded_path"] != decodedPaths[0] {
		t.Fatalf("decoded detail = %#v", d)
	}
	if d["mime_type"] != "image/png" || d["width"] != 1 || d["height"] != 1 {
		t.Fatalf("decoded image metadata = %#v", d)
	}
}

func TestWriteDecodedMediaCacheStrictReadOnlyDoesNotCreateCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")

	srv := &server{cfg: &config.Config{Wxid: "wxid_test"}}
	_, err := srv.writeDecodedMediaCache("sample.dat", tinyPNG(), "png")
	if err == nil || !strings.Contains(err.Error(), "strict_read_only") {
		t.Fatalf("writeDecodedMediaCache error = %v, want strict_read_only", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".wechat-cli")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strict read-only created state dir or unexpected stat error: %v", err)
	}
}

func TestDecodeLocalImageForAgentReportsMissingImageKey(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("WX_MCP_CONFIG", cfgPath)
	root := t.TempDir()
	key := []byte("abcdefghijklmnop")
	img := filepath.Join(root, "sample.dat")
	if err := os.WriteFile(img, testWechatV4ImageDAT(t, key, tinyPNG()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(&config.Config{DBRoot: root, Keys: map[string]string{"salt": "enc"}}); err != nil {
		t.Fatal(err)
	}
	oldRunImageKey := runWxkeyImageKey
	t.Cleanup(func() { runWxkeyImageKey = oldRunImageKey })
	runWxkeyImageKey = func(rootArg string) (*wxkey.ImageKeyResult, string, error) {
		return nil, "", os.ErrNotExist
	}
	srv := &server{cfg: &config.Config{DBRoot: root}}
	res := map[string]any{}
	srv.attachLocalPathAgentFields(res, "image", []string{img})

	if direct, ok := res["direct_readable"].(bool); !ok || direct {
		t.Fatalf("direct_readable = %#v, want false without image key", res["direct_readable"])
	}
	details, ok := res["local_path_details"].([]map[string]any)
	if !ok || len(details) != 1 {
		t.Fatalf("local_path_details = %#v, want one detail", res["local_path_details"])
	}
	if details[0]["decode_status"] != "needs_image_key" {
		t.Fatalf("decode_status = %#v, want needs_image_key; detail=%#v", details[0]["decode_status"], details[0])
	}
}

func TestDecodeLocalImageStrictReadOnlySkipsImageKeyRefresh(t *testing.T) {
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("WX_MCP_CONFIG", cfgPath)
	root := t.TempDir()
	key := []byte("abcdefghijklmnop")
	img := filepath.Join(root, "sample.dat")
	if err := os.WriteFile(img, testWechatV4ImageDAT(t, key, tinyPNG()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(&config.Config{DBRoot: root, Keys: map[string]string{"salt": "enc"}}); err != nil {
		t.Fatal(err)
	}
	oldRunImageKey := runWxkeyImageKey
	t.Cleanup(func() { runWxkeyImageKey = oldRunImageKey })
	calls := 0
	runWxkeyImageKey = func(rootArg string) (*wxkey.ImageKeyResult, string, error) {
		calls++
		return &wxkey.ImageKeyResult{Key: hex.EncodeToString(key)}, "", nil
	}

	srv := &server{cfg: &config.Config{DBRoot: root}}
	res := map[string]any{}
	srv.attachLocalPathAgentFields(res, "image", []string{img})

	if calls != 0 {
		t.Fatalf("image-key refresh calls = %d, want 0 in strict read-only", calls)
	}
	details, ok := res["local_path_details"].([]map[string]any)
	if !ok || len(details) != 1 {
		t.Fatalf("local_path_details = %#v, want one detail", res["local_path_details"])
	}
	if details[0]["image_key_refresh"] != "failed" || !strings.Contains(fmt.Sprint(details[0]["image_key_refresh_error"]), "strict_read_only") {
		t.Fatalf("image-key refresh detail = %#v", details[0])
	}
}

func TestDecodeLocalImageForAgentAutoRefreshesImageKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("WX_MCP_CONFIG", cfgPath)
	root := t.TempDir()
	key := []byte("abcdefghijklmnop")
	img := filepath.Join(root, "msg", "attach", "hash", "2026-05", "Img", "sample.dat")
	if err := os.MkdirAll(filepath.Dir(img), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(img, testWechatV4ImageDAT(t, key, tinyPNG()), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Wxid:   "wxid_test",
		DBRoot: root,
		Keys:   map[string]string{"salt": "enc"},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	oldRunImageKey := runWxkeyImageKey
	t.Cleanup(func() { runWxkeyImageKey = oldRunImageKey })
	calls := 0
	runWxkeyImageKey = func(rootArg string) (*wxkey.ImageKeyResult, string, error) {
		calls++
		if rootArg != root {
			t.Fatalf("image-key root = %q, want %q", rootArg, root)
		}
		return &wxkey.ImageKeyResult{Key: hex.EncodeToString(key)}, "", nil
	}
	srv := &server{cfg: cfg, ok: true}
	res := map[string]any{}
	srv.attachLocalPathAgentFields(res, "image", []string{img})

	if calls != 1 {
		t.Fatalf("image-key refresh calls = %d, want 1", calls)
	}
	if direct, ok := res["direct_readable"].(bool); !ok || !direct {
		t.Fatalf("direct_readable = %#v, want true after auto image_key refresh", res["direct_readable"])
	}
	decodedPaths, ok := res["decoded_local_paths"].([]string)
	if !ok || len(decodedPaths) != 1 {
		t.Fatalf("decoded_local_paths = %#v, want one decoded path", res["decoded_local_paths"])
	}
	if _, err := os.Stat(decodedPaths[0]); err != nil {
		t.Fatalf("decoded path is not readable: %v", err)
	}
	fresh, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ImageKey != hex.EncodeToString(key) {
		t.Fatalf("saved image_key = %q, want refreshed key", fresh.ImageKey)
	}
}

func TestImageKeyCandidatesIncludesConfigWhenEnvSet(t *testing.T) {
	key := []byte("abcdefghijklmnop")
	t.Setenv("WX_MCP_IMAGE_KEY", "badbadbadbadbadb")
	srv := &server{cfg: &config.Config{ImageKey: hex.EncodeToString(key)}}
	candidates := srv.imageKeyCandidates()
	if len(candidates) < 2 {
		t.Fatalf("imageKeyCandidates len=%d, want env and config candidates", len(candidates))
	}
	var sawConfig bool
	for _, cand := range candidates {
		if cand.source == "config_hex" && bytes.Equal(cand.key, key) {
			sawConfig = true
		}
	}
	if !sawConfig {
		t.Fatalf("config image_key candidate missing when env is set: %#v", candidates)
	}
}

func TestAttachMediaResourceRowsToMessagesAddsLocalPaths(t *testing.T) {
	root := t.TempDir()
	srv := &server{cfg: &config.Config{DBRoot: root}}
	talker := "wxid_media_test"
	ts := time.Date(2026, 5, 9, 12, 0, 0, 0, time.Local).Unix()
	md5 := "fe8776339cd67e6023d7e47b97b073a0"
	img := filepath.Join(root, "msg", "attach", talkerHash(talker), "2026-05", "Img", md5+".dat")
	if err := os.MkdirAll(filepath.Dir(img), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(img, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	md5Blob, err := hex.DecodeString("12220A206665383737363333396364363765363032336437653437623937623037336130")
	if err != nil {
		t.Fatal(err)
	}

	messages := []wcdb.Row{{
		"talker":    talker,
		"local_id":  int64(7),
		"server_id": int64(7710666891970547832),
		"base_kind": int64(3),
		"kind_name": "image",
		"message_content_parsed": map[string]any{
			"aeskey":        "abc123",
			"cdn_big_url":   "wechat-big-id",
			"cdn_thumb_url": "wechat-thumb-id",
			"length":        int64(3),
		},
	}}
	resourceRows := []wcdb.Row{{
		"talker":               talker,
		"message_local_id":     int64(7),
		"message_svr_id":       int64(7710666891970547832),
		"message_local_type":   int64(3),
		"message_create_time":  ts,
		"message_packed_info":  md5Blob,
		"resource_id":          int64(11),
		"resource_type_raw":    int64(65537),
		"resource_size":        int64(3),
		"resource_status":      int64(1),
		"resource_data_index":  "0",
		"resource_packed_info": md5Blob,
	}}

	srv.attachMediaResourceRowsToMessages(messages, resourceRows)
	attachMediaReadHintsToMessages(messages)
	paths, ok := messages[0]["media_local_paths"].([]string)
	if !ok || len(paths) != 1 || paths[0] != img {
		t.Fatalf("media_local_paths = %#v, want [%q]", messages[0]["media_local_paths"], img)
	}
	uris, ok := messages[0]["media_local_path_uris"].([]string)
	if !ok || len(uris) != 1 || uris[0] != localFileURI(img) {
		t.Fatalf("media_local_path_uris = %#v, want [%q]", messages[0]["media_local_path_uris"], localFileURI(img))
	}
	resources, ok := messages[0]["media_resources"].([]map[string]any)
	if !ok || len(resources) != 1 {
		t.Fatalf("media_resources = %#v, want one resource", messages[0]["media_resources"])
	}
	if _, ok := resources[0]["local_paths"]; ok {
		t.Fatalf("message media_resources should stay compact and omit local_paths: %#v", resources[0])
	}
	if n, ok := resources[0]["local_path_count"].(int); !ok || n != 1 {
		t.Fatalf("local_path_count = %#v, want 1", resources[0]["local_path_count"])
	}
	if direct, ok := resources[0]["direct_readable"].(bool); !ok || direct {
		t.Fatalf("direct_readable = %#v, want false for WeChat image .dat", resources[0]["direct_readable"])
	}
	formats, ok := resources[0]["storage_formats"].([]string)
	if !ok || len(formats) != 1 || formats[0] != "wechat_image_dat" {
		t.Fatalf("storage_formats = %#v, want [wechat_image_dat]", resources[0]["storage_formats"])
	}
	hints, ok := messages[0]["media_read_hints"].([]map[string]any)
	if !ok || len(hints) != 2 {
		t.Fatalf("media_read_hints = %#v, want resource + XML hints", messages[0]["media_read_hints"])
	}
	if hints[0]["address_type"] != "local_file" || hints[0]["direct_readable"] != false {
		t.Fatalf("local media_read_hint = %#v", hints[0])
	}
	hintPaths, ok := hints[0]["local_paths"].([]string)
	if !ok || len(hintPaths) != 1 || hintPaths[0] != img {
		t.Fatalf("hint local_paths = %#v, want [%q]", hints[0]["local_paths"], img)
	}
	hintURIs, ok := hints[0]["local_path_uris"].([]string)
	if !ok || len(hintURIs) != 1 || hintURIs[0] != localFileURI(img) {
		t.Fatalf("hint local_path_uris = %#v, want [%q]", hints[0]["local_path_uris"], localFileURI(img))
	}
	details, ok := hints[0]["local_path_details"].([]map[string]any)
	if !ok || len(details) != 1 || details[0]["storage_format"] != "wechat_image_dat" {
		t.Fatalf("hint local_path_details = %#v, want wechat_image_dat detail", hints[0]["local_path_details"])
	}
	if hints[1]["address_type"] != "wechat_cdn" || hints[1]["aeskey"] != "abc123" {
		t.Fatalf("XML media_read_hint = %#v", hints[1])
	}
	cdn, ok := hints[1]["wechat_cdn"].(map[string]any)
	if !ok || cdn["big_url"] != "wechat-big-id" || cdn["thumb_url"] != "wechat-thumb-id" {
		t.Fatalf("wechat_cdn = %#v", hints[1]["wechat_cdn"])
	}
}

func TestAttachMediaResourceRowsToMessagesDoesNotAttachLocalIDCollision(t *testing.T) {
	root := t.TempDir()
	srv := &server{cfg: &config.Config{DBRoot: root}}
	talker := "wxid_media_test"
	messages := []wcdb.Row{{
		"talker":      talker,
		"local_id":    int64(7),
		"server_id":   int64(200),
		"create_time": int64(1779433702),
		"base_kind":   int64(1),
		"kind_name":   "text",
	}}
	resourceRows := []wcdb.Row{{
		"talker":              talker,
		"message_local_id":    int64(7),
		"message_svr_id":      int64(100),
		"message_local_type":  int64(3),
		"message_create_time": int64(1769588358),
		"resource_id":         int64(11),
		"resource_type_raw":   int64(65537),
		"resource_size":       int64(3),
		"resource_status":     int64(1),
	}}

	srv.attachMediaResourceRowsToMessages(messages, resourceRows)
	if _, ok := messages[0]["media_resources"]; ok {
		t.Fatalf("media_resources should not attach a reused local_id from another server/time: %#v", messages[0]["media_resources"])
	}
}

func TestAttachMediaResourceRowsToMessagesUsesMessageContentMD5ForReadableImage(t *testing.T) {
	root := t.TempDir()
	srv := &server{cfg: &config.Config{DBRoot: root}}
	talker := "wxid_media_test"
	ts := time.Date(2026, 5, 22, 15, 8, 22, 0, time.Local).Unix()
	resourceMD5 := "f3271f47e86d4f0b59bc3edb899eb68d"
	imageBytes := tinyPNG()
	sum := md5.Sum(imageBytes)
	contentMD5 := hex.EncodeToString(sum[:])

	direct := filepath.Join(root, "temp", "InputTemp", "direct.png")
	if err := os.MkdirAll(filepath.Dir(direct), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(direct, imageBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	imgDAT := filepath.Join(root, "msg", "attach", talkerHash(talker), "2026-05", "Img", resourceMD5+".dat")
	if err := os.MkdirAll(filepath.Dir(imgDAT), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imgDAT, []byte("encrypted"), 0o644); err != nil {
		t.Fatal(err)
	}
	md5Blob, err := hex.DecodeString("12220A206633323731663437653836643466306235396263336564623839396562363864")
	if err != nil {
		t.Fatal(err)
	}

	messages := []wcdb.Row{{
		"talker":      talker,
		"local_id":    int64(7),
		"create_time": ts,
		"base_kind":   int64(3),
		"kind_name":   "image",
		"message_content_parsed": map[string]any{
			"md5": contentMD5,
		},
	}}
	resourceRows := []wcdb.Row{{
		"talker":               talker,
		"message_local_id":     int64(7),
		"message_local_type":   int64(3),
		"message_create_time":  ts,
		"message_packed_info":  md5Blob,
		"resource_id":          int64(11),
		"resource_type_raw":    int64(65537),
		"resource_size":        int64(len(imageBytes)),
		"resource_status":      int64(1),
		"resource_data_index":  "0",
		"resource_packed_info": md5Blob,
	}}

	srv.attachMediaResourceRowsToMessages(messages, resourceRows)
	attachMediaReadHintsToMessages(messages)
	paths := stringSliceAny(messages[0]["media_local_paths"])
	if len(paths) != 2 || paths[0] != direct || paths[1] != imgDAT {
		t.Fatalf("media_local_paths = %#v, want direct PNG then .dat", messages[0]["media_local_paths"])
	}
	resources, ok := messages[0]["media_resources"].([]map[string]any)
	if !ok || len(resources) != 1 {
		t.Fatalf("media_resources = %#v, want one resource", messages[0]["media_resources"])
	}
	if resources[0]["direct_readable"] != true || resources[0]["content_md5"] != contentMD5 {
		t.Fatalf("compact resource should expose direct readable content md5: %#v", resources[0])
	}
	hints, ok := messages[0]["media_read_hints"].([]map[string]any)
	if !ok || len(hints) < 1 {
		t.Fatalf("media_read_hints = %#v, want at least one local hint", messages[0]["media_read_hints"])
	}
	directPaths := stringSliceAny(hints[0]["direct_readable_local_paths"])
	if hints[0]["direct_readable"] != true || len(directPaths) != 1 || directPaths[0] != direct {
		t.Fatalf("direct readable hint = %#v", hints[0])
	}
}

func TestQuoteImageMediaReadHintsExposeDirectReadablePath(t *testing.T) {
	root := t.TempDir()
	srv := &server{cfg: &config.Config{DBRoot: root}}
	imageBytes := tinyPNG()
	sum := md5.Sum(imageBytes)
	contentMD5 := hex.EncodeToString(sum[:])
	direct := filepath.Join(root, "temp", "InputTemp", "quoted.png")
	if err := os.MkdirAll(filepath.Dir(direct), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(direct, imageBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	refer := map[string]any{
		"type":        3,
		"createtime":  int64(1779433050),
		"displayname": "V",
		"fromusr":     "45428231044@chatroom",
		"content_parsed": map[string]any{
			"md5":           contentMD5,
			"aeskey":        "refer-aeskey",
			"cdn_big_url":   "refer-big-url",
			"cdn_thumb_url": "refer-thumb-url",
			"length":        int64(len(imageBytes)),
		},
	}
	messages := []wcdb.Row{{
		"talker":      "45428231044@chatroom",
		"local_id":    int64(641),
		"create_time": int64(1779433700),
		"base_kind":   int64(49),
		"subtype":     int64(57),
		"kind_name":   "quote",
		"message_content_parsed": map[string]any{
			"title":    "reply title",
			"refermsg": refer,
		},
	}}

	srv.attachMediaReadHintsToMessages(messages)
	hints, ok := messages[0]["media_read_hints"].([]map[string]any)
	if !ok || len(hints) != 2 {
		t.Fatalf("media_read_hints = %#v, want direct + CDN refer hints", messages[0]["media_read_hints"])
	}
	if hints[0]["source"] != "message_refermsg" || hints[0]["message_role"] != "referenced_message" || hints[0]["direct_readable"] != true {
		t.Fatalf("direct refer hint = %#v", hints[0])
	}
	paths := stringSliceAny(hints[0]["direct_readable_local_paths"])
	if len(paths) != 1 || paths[0] != direct {
		t.Fatalf("direct_readable_local_paths = %#v, want [%q]", paths, direct)
	}
	if hints[1]["address_type"] != "wechat_cdn" || hints[1]["aeskey"] != "refer-aeskey" {
		t.Fatalf("refer CDN hint = %#v", hints[1])
	}
	nestedHints, ok := refer["media_read_hints"].([]map[string]any)
	if !ok || len(nestedHints) != 2 {
		t.Fatalf("refer media_read_hints = %#v, want nested hints", refer["media_read_hints"])
	}
}

func testWechatV4ImageDAT(t *testing.T, key, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytesRepeat(byte(padding), padding)...)
	encrypted := make([]byte, len(padded))
	for start := 0; start < len(padded); start += aes.BlockSize {
		block.Encrypt(encrypted[start:start+aes.BlockSize], padded[start:start+aes.BlockSize])
	}
	header := make([]byte, 15)
	copy(header, wechatV4ImageHeader2)
	binary.LittleEndian.PutUint32(header[6:10], uint32(len(plain)))
	binary.LittleEndian.PutUint32(header[10:14], 0)
	header[14] = 1
	return append(header, encrypted...)
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func tinyPNG() []byte {
	b, _ := hex.DecodeString("89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c4890000000a49444154789c6360000000020001e221bc330000000049454e44ae426082")
	return b
}

func TestBoundedReadSQL(t *testing.T) {
	got, err := boundedReadSQL("SELECT id FROM t ORDER BY id DESC", 10)
	if err != nil {
		t.Fatalf("boundedReadSQL returned error: %v", err)
	}
	want := "SELECT * FROM (SELECT id FROM t ORDER BY id DESC) LIMIT 10"
	if got != want {
		t.Fatalf("boundedReadSQL = %q, want %q", got, want)
	}

	if _, err := boundedReadSQL("DELETE FROM t", 10); err == nil {
		t.Fatalf("boundedReadSQL should reject writes")
	}
	if _, err := boundedReadSQL("SELECT 1; SELECT 2", 10); err == nil {
		t.Fatalf("boundedReadSQL should reject multiple statements")
	}
}

func TestAcquireCacheRefreshLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	unlock, acquired, lockPath, err := acquireCacheRefreshLock()
	if err != nil {
		t.Fatalf("acquireCacheRefreshLock returned error: %v", err)
	}
	if !acquired {
		t.Fatalf("first lock acquire should succeed")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".wechat-cli", "cache-refresh.lock")); err != nil {
		t.Fatalf("lock dir missing at %s: %v", lockPath, err)
	}
	if _, err := os.Stat(filepath.Join(lockPath, cacheRefreshLockOwnerFile)); err != nil {
		t.Fatalf("lock owner missing: %v", err)
	}

	_, acquired2, _, err := acquireCacheRefreshLock()
	if err != nil {
		t.Fatalf("second acquire returned error: %v", err)
	}
	if acquired2 {
		t.Fatalf("second acquire should report busy")
	}
	unlock()

	_, err = os.Stat(lockPath)
	if !os.IsNotExist(err) {
		t.Fatalf("lock should be removed after unlock, stat err=%v", err)
	}
}

func TestAcquireCacheRefreshLockRecoversLegacyEmptyLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	lockPath := filepath.Join(home, ".wechat-cli", "cache-refresh.lock")
	if err := os.MkdirAll(lockPath, 0o700); err != nil {
		t.Fatalf("mkdir legacy lock: %v", err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("chtimes legacy lock: %v", err)
	}

	unlock, acquired, gotPath, err := acquireCacheRefreshLock()
	if err != nil {
		t.Fatalf("acquire legacy lock returned error: %v", err)
	}
	if !acquired || gotPath != lockPath {
		t.Fatalf("acquired=%v path=%q, want acquired legacy path %q", acquired, gotPath, lockPath)
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(lockPath, cacheRefreshLockOwnerFile)); err != nil {
		t.Fatalf("new owner missing after legacy recovery: %v", err)
	}
}
