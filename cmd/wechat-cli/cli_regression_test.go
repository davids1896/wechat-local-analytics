package main

import (
	"strings"
	"testing"

	"github.com/r266-tech/wechat-cli/internal/wcdb"
)

func TestReadMessageEventArgsCatchUpUsesOldestUnseenPage(t *testing.T) {
	args, catchUp, err := readMessageEventArgs(map[string]any{
		"chat":   "room@chatroom",
		"cursor": "local_id:1",
		"limit":  int64(2),
	})
	if err != nil {
		t.Fatalf("readMessageEventArgs error: %v", err)
	}
	if !catchUp || args["after_message"] != int64(1) {
		t.Fatalf("catch-up args = %#v", args)
	}
	if args["order"] != "asc" || args["display_order"] != "query" {
		t.Fatalf("catch-up order = (%#v,%#v), want asc/query", args["order"], args["display_order"])
	}

	initial, catchUp, err := readMessageEventArgs(map[string]any{"chat": "room@chatroom", "limit": int64(2)})
	if err != nil {
		t.Fatalf("initial readMessageEventArgs error: %v", err)
	}
	if catchUp || initial["order"] != "desc" || initial["display_order"] != "asc" {
		t.Fatalf("initial args = %#v", initial)
	}
}

func TestSessionEventBatchDrainsBacklogWithoutGaps(t *testing.T) {
	rows := []map[string]any{
		{"username": "u5", "last_timestamp": int64(5)},
		{"username": "u4", "last_timestamp": int64(4)},
		{"username": "u3", "last_timestamp": int64(3)},
		{"username": "u2", "last_timestamp": int64(2)},
	}
	first, hasMore := buildSessionEventBatch(rows, sessionReadEventsCursor{Timestamp: 1}, true, 2)
	if !hasMore || len(first) != 2 {
		t.Fatalf("first batch len/has_more = %d/%v, events=%#v", len(first), hasMore, first)
	}
	if got := sessionEventTimestamps(first); got[0] != 2 || got[1] != 3 {
		t.Fatalf("first batch timestamps = %v, want [2 3]", got)
	}
	cursor, err := parseSessionReadEventsCursor(newestReadEventsCursor(first))
	if err != nil {
		t.Fatalf("parse first cursor: %v", err)
	}
	second, hasMore := buildSessionEventBatch(rows, cursor, true, 2)
	if hasMore || len(second) != 2 {
		t.Fatalf("second batch len/has_more = %d/%v, events=%#v", len(second), hasMore, second)
	}
	if got := sessionEventTimestamps(second); got[0] != 4 || got[1] != 5 {
		t.Fatalf("second batch timestamps = %v, want [4 5]", got)
	}
}

func TestSessionEventCursorBreaksTimestampTies(t *testing.T) {
	rows := []map[string]any{
		{"username": "c", "last_timestamp": int64(10)},
		{"username": "b", "last_timestamp": int64(10)},
		{"username": "a", "last_timestamp": int64(10)},
	}
	first, hasMore := buildSessionEventBatch(rows, sessionReadEventsCursor{Timestamp: 9}, true, 2)
	if !hasMore || sessionEventUsername(first[0]) != "a" || sessionEventUsername(first[1]) != "b" {
		t.Fatalf("first tied batch = %#v, has_more=%v", first, hasMore)
	}
	cursor, err := parseSessionReadEventsCursor(newestReadEventsCursor(first))
	if err != nil {
		t.Fatalf("parse tied cursor: %v", err)
	}
	second, hasMore := buildSessionEventBatch(rows, cursor, true, 2)
	if hasMore || len(second) != 1 || sessionEventUsername(second[0]) != "c" {
		t.Fatalf("second tied batch = %#v, has_more=%v", second, hasMore)
	}
}

func TestDecodeCLIJSONArgsPreservesInt64(t *testing.T) {
	const want = int64(7710666891970547832)
	args, err := decodeCLIJSONArgs(`{"server_id":7710666891970547832,"limit":1}`)
	if err != nil {
		t.Fatalf("decodeCLIJSONArgs error: %v", err)
	}
	if got, ok := args["server_id"].(int64); !ok || got != want {
		t.Fatalf("server_id = %#v (%T), want exact int64 %d", args["server_id"], args["server_id"], want)
	}
	if _, err := decodeCLIJSONArgs(`{"server_id":9e18}`); err == nil {
		t.Fatal("unsafe exponent-form integer should be rejected")
	}
}

func TestCLIRowsEnvelopeUsesSentinelAndKeepsEmptyList(t *testing.T) {
	withMore := cliAgentDataEnvelope("search", "search", map[string]any{
		"keyword": "x",
		"limit":   int64(1),
	}, cliRowsResult{
		Rows:      []wcdb.Row{{"local_id": int64(1)}, {"local_id": int64(2)}},
		Freshness: map[string]any{"message_source": "live_message_fts_db"},
		Warnings:  []string{"metadata_cache_stale"},
	}).(map[string]any)
	query := withMore["query"].(map[string]any)
	if query["returned"] != 1 || query["has_more"] != true || query["next_offset"] != 1 {
		t.Fatalf("sentinel query = %#v", query)
	}
	if messages := withMore["messages"].([]map[string]any); len(messages) != 1 {
		t.Fatalf("sentinel messages = %#v", messages)
	}
	if withMore["freshness"].(map[string]any)["message_source"] != "live_message_fts_db" {
		t.Fatalf("freshness not forwarded: %#v", withMore)
	}
	if warnings := withMore["warnings"].([]string); len(warnings) != 1 {
		t.Fatalf("warnings not forwarded: %#v", warnings)
	}

	exactEnd := cliAgentDataEnvelope("media_resources", "media", map[string]any{"limit": int64(1)}, cliRowsResult{
		Rows: []wcdb.Row{{"local_id": int64(1)}},
	}).(map[string]any)
	exactQuery := exactEnd["query"].(map[string]any)
	if exactQuery["has_more"] != false {
		t.Fatalf("exact terminal page has_more = %#v, want false", exactQuery["has_more"])
	}

	empty := cliAgentDataEnvelope("media_resources", "media", map[string]any{"limit": int64(1)}, cliRowsResult{}).(map[string]any)
	media, ok := empty["media"].([]map[string]any)
	if !ok || len(media) != 0 {
		t.Fatalf("empty media list missing or unstable: %#v", empty)
	}
	emptyQuery := empty["query"].(map[string]any)
	if emptyQuery["returned"] != 0 || emptyQuery["offset"] != 0 || emptyQuery["has_more"] != false {
		t.Fatalf("empty query metadata is unstable: %#v", emptyQuery)
	}

	partial := cliAgentDataEnvelope("search", "search", map[string]any{"limit": int64(1)}, cliRowsResult{
		Freshness: map[string]any{"complete": false},
		Warnings:  []string{"search_scan_truncated_after_50000_rows"},
	}).(map[string]any)
	partialQuery := partial["query"].(map[string]any)
	if partialQuery["has_more_unknown"] != true {
		t.Fatalf("partial query missing has_more_unknown: %#v", partialQuery)
	}
	if _, ok := partialQuery["has_more"]; ok {
		t.Fatalf("partial query claims a definite terminal state: %#v", partialQuery)
	}
}

func TestCLIToolFetchArgsRequestsSentinelWithoutMutatingRequest(t *testing.T) {
	request := map[string]any{"keyword": "x", "limit": int64(20)}
	fetch := cliToolFetchArgs("search", request)
	if fetch["limit"] != int64(21) {
		t.Fatalf("fetch limit = %#v, want 21", fetch["limit"])
	}
	if request["limit"] != int64(20) {
		t.Fatalf("request mutated: %#v", request)
	}
}

func TestCLIToolResponseArgsMaterializesDefaultLimit(t *testing.T) {
	request := map[string]any{"keyword": "x"}
	response := cliToolResponseArgs("search", request)
	if response["limit"] != int64(20) {
		t.Fatalf("response limit = %#v, want 20", response["limit"])
	}
	fetch := cliToolFetchArgs("search", response)
	if fetch["limit"] != int64(21) {
		t.Fatalf("fetch limit = %#v, want 21", fetch["limit"])
	}
	if _, ok := request["limit"]; ok {
		t.Fatalf("request mutated: %#v", request)
	}
}

func TestCallJSONExamplesDoNotHideShellVariablesInSingleQuotes(t *testing.T) {
	for _, spec := range cliCommandSpecs {
		if spec.Command != "call-json" {
			continue
		}
		for _, example := range spec.Examples {
			for rest := example; ; {
				start := strings.IndexByte(rest, '\'')
				if start < 0 {
					break
				}
				rest = rest[start+1:]
				end := strings.IndexByte(rest, '\'')
				if end < 0 {
					break
				}
				segment := rest[:end]
				if strings.HasPrefix(strings.TrimSpace(segment), "{") && strings.Contains(segment, "$") {
					t.Fatalf("call-json example hides shell variable in single-quoted JSON: %s", example)
				}
				rest = rest[end+1:]
			}
		}
	}
}

func sessionEventTimestamps(events []map[string]any) []int64 {
	out := make([]int64, 0, len(events))
	for _, event := range events {
		session, _ := event["session"].(map[string]any)
		out = append(out, int64MapValue(session, "last_timestamp"))
	}
	return out
}

func sessionEventUsername(event map[string]any) string {
	session, _ := event["session"].(map[string]any)
	return stringMapValue(session, "username")
}
