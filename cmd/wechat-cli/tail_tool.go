package main

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r266-tech/wechat-cli/internal/wcdb"
)

func (s *server) toolReadEvents(a map[string]any) (any, error) {
	mode := strings.TrimSpace(strings.ToLower(getStr(a, "mode")))
	if mode == "" || mode == "auto" {
		if firstNonEmpty(getStr(a, "chat"), getStr(a, "talker")) != "" {
			mode = "messages"
		} else {
			mode = "sessions"
		}
	}
	switch mode {
	case "messages":
		return s.readMessageEvents(a)
	case "sessions":
		return s.readSessionEvents(a)
	default:
		return nil, fmt.Errorf("invalid mode=%q: must be messages or sessions", mode)
	}
}

func (s *server) readMessageEvents(a map[string]any) (any, error) {
	args, catchUp, err := readMessageEventArgs(a)
	if err != nil {
		return nil, err
	}

	raw, err := s.toolChatTimeline(args)
	if err != nil {
		return nil, err
	}
	env, _ := raw.(map[string]any)
	messages := mapSliceAny(env["messages"])
	events := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		event := compactMap(map[string]any{
			"type":       "message",
			"event_time": msg["time_iso"],
			"message":    msg,
		})
		if id := mapStringAny(msg["id"]); len(id) > 0 {
			event["cursor"] = readEventsCursorFromMessageID(id)
		}
		events = append(events, event)
	}
	timelineQuery := mapStringAny(env["query"])
	hasMore := catchUp && getBoolDefault(timelineQuery, "has_more", false)
	nextCursor := newestReadEventsCursor(events)
	if nextCursor == "" {
		nextCursor = currentMessageReadEventsCursor(args)
	}
	out := compactMap(map[string]any{
		"query": compactMap(map[string]any{
			"mode":     "messages",
			"chat":     firstNonEmpty(getStr(a, "chat"), getStr(a, "talker")),
			"from_me":  queryFromMeArg(a),
			"limit":    getInt(args, "limit", 50),
			"cursor":   getStr(a, "cursor"),
			"returned": len(events),
			"has_more": hasMore,
		}),
		"freshness": env["freshness"],
		"warnings":  env["warnings"],
		"cursor":    nextCursor,
	})
	out["events"] = events
	return out, nil
}

func readMessageEventArgs(a map[string]any) (map[string]any, bool, error) {
	args := copyToolArgs(a)
	if args["limit"] == nil {
		args["limit"] = int64(50)
	}
	if v := firstNonEmpty(getStr(args, "since_time"), getStr(args, "since")); v != "" && args["after"] == nil {
		args["after"] = v
	}
	if id, ok, err := int64Arg(args, "since_local_id"); err != nil {
		return nil, false, err
	} else if ok && args["after_message"] == nil {
		args["after_message"] = id
	}
	if cursor := getStr(args, "cursor"); cursor != "" && args["after_message"] == nil && args["after"] == nil {
		applyReadEventsCursor(args, cursor)
		if args["after_message"] == nil && getStr(args, "after") == "" {
			return nil, false, fmt.Errorf("invalid message cursor %q", cursor)
		}
	}
	catchUp := args["after_message"] != nil || getStr(args, "after") != ""
	if catchUp {
		// Catch-up reads must take the oldest unseen rows first. Taking a DESC
		// page and advancing to its newest row permanently skips the middle of a
		// backlog when more than limit messages arrived between polls.
		args["order"] = "asc"
		args["display_order"] = "query"
	} else {
		// With no starting cursor, preserve tail's snapshot behavior: return the
		// latest N messages displayed chronologically.
		args["order"] = "desc"
		args["display_order"] = "asc"
	}
	return args, catchUp, nil
}

func (s *server) readSessionEvents(a map[string]any) (any, error) {
	limit := getInt(a, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	// Sessions has no offset/keyset API. Scan the full supported window by
	// default so a cursor catches up from the oldest unseen session rather than
	// sampling only the newest limit rows and skipping the middle.
	scanLimit := getInt(a, "scan_limit", 5000)
	if scanLimit < limit {
		scanLimit = limit
	}
	args := copyToolArgs(a)
	args["limit"] = int64(scanLimit + 1)

	raw, err := s.toolSessions(args)
	if err != nil {
		return nil, err
	}
	rows, ok := cliResultRows(raw)
	if !ok {
		return nil, fmt.Errorf("read_events internal error: sessions returned %T", raw)
	}
	freshness, warnings := cliRowsResultMetadata(raw)
	cursor, catchUp, err := readEventsSessionCursor(a)
	if err != nil {
		return nil, err
	}
	scanTruncated := len(rows) > scanLimit
	if scanTruncated {
		rows = rows[:scanLimit]
		warnings = appendUniqueStrings(warnings, "session_scan_truncated")
	}
	var events []map[string]any
	hasMore := false
	if scanTruncated && catchUp {
		// The omitted rows may sit between the caller's cursor and this scan
		// window. Returning newer rows would create an unrecoverable gap, so keep
		// the cursor unchanged and report the bounded-scan condition instead.
		events = make([]map[string]any, 0)
		hasMore = true
	} else {
		events, hasMore = buildSessionEventBatch(rows, cursor, catchUp, limit)
	}
	if freshness == nil {
		freshness = map[string]any{
			"message_source":      "metadata_cache_sessions",
			"metadata_cache_role": "session/unread observation",
		}
	}
	nextCursor := newestReadEventsCursor(events)
	if nextCursor == "" {
		nextCursor = encodeSessionReadEventsCursor(cursor)
	}
	query := compactMap(map[string]any{
		"mode":           "sessions",
		"limit":          limit,
		"scan_limit":     scanLimit,
		"cursor":         getStr(a, "cursor"),
		"returned":       len(events),
		"has_more":       hasMore,
		"scan_truncated": scanTruncated,
	})
	out := compactMap(map[string]any{
		"query":     query,
		"freshness": freshness,
		"warnings":  warnings,
		"cursor":    nextCursor,
	})
	// Event list shape is stable even when no new rows are available.
	out["events"] = events
	return out, nil
}

type sessionReadEventsCursor struct {
	Timestamp int64
	Username  string
}

type sessionEventCandidate struct {
	Timestamp int64
	Username  string
	Row       map[string]any
}

func buildSessionEventBatch(rows []map[string]any, cursor sessionReadEventsCursor, catchUp bool, limit int) ([]map[string]any, bool) {
	candidates := make([]sessionEventCandidate, 0, len(rows))
	for _, row := range rows {
		candidate := sessionEventCandidate{
			Timestamp: firstNonZeroInt64(int64MapValue(row, "last_timestamp"), int64MapValue(row, "sort_timestamp")),
			Username:  stringMapValue(row, "username"),
			Row:       row,
		}
		if candidate.Timestamp <= 0 {
			continue
		}
		if catchUp && !sessionEventCandidateAfter(candidate, cursor) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Timestamp != candidates[j].Timestamp {
			return candidates[i].Timestamp < candidates[j].Timestamp
		}
		return candidates[i].Username < candidates[j].Username
	})
	hasMore := false
	if catchUp {
		if len(candidates) > limit {
			hasMore = true
			candidates = candidates[:limit]
		}
	} else if len(candidates) > limit {
		// An initial tail call establishes a cursor at the latest snapshot; older
		// sessions are history, not an incremental backlog for that cursor.
		candidates = candidates[len(candidates)-limit:]
	}
	events := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		events = append(events, compactMap(map[string]any{
			"type":       "session",
			"event_time": cliFormatUnixISO(candidate.Timestamp),
			"cursor": encodeSessionReadEventsCursor(sessionReadEventsCursor{
				Timestamp: candidate.Timestamp,
				Username:  candidate.Username,
			}),
			"session": candidate.Row,
		}))
	}
	return events, hasMore
}

func sessionEventCandidateAfter(candidate sessionEventCandidate, cursor sessionReadEventsCursor) bool {
	if candidate.Timestamp != cursor.Timestamp {
		return candidate.Timestamp > cursor.Timestamp
	}
	if cursor.Username == "" {
		// Legacy timestamp-only cursors mean the whole timestamp was consumed.
		return false
	}
	return candidate.Username > cursor.Username
}

func applyReadEventsCursor(args map[string]any, cursor string) {
	cursor = strings.TrimSpace(cursor)
	switch {
	case strings.HasPrefix(cursor, "local_id:"):
		if n, err := strconv.ParseInt(strings.TrimPrefix(cursor, "local_id:"), 10, 64); err == nil && n > 0 {
			args["after_message"] = n
		}
	case strings.HasPrefix(cursor, "message:"):
		if n, err := strconv.ParseInt(strings.TrimPrefix(cursor, "message:"), 10, 64); err == nil && n > 0 {
			args["after_message"] = n
		}
	case strings.HasPrefix(cursor, "time:"):
		args["after"] = strings.TrimPrefix(cursor, "time:")
	}
}

func readEventsCursorFromMessageID(id map[string]any) string {
	if n := int64MapValue(id, "local_id"); n > 0 {
		return fmt.Sprintf("local_id:%d", n)
	}
	return ""
}

func currentMessageReadEventsCursor(args map[string]any) string {
	if cursor := getStr(args, "cursor"); cursor != "" {
		return cursor
	}
	if id, ok, _ := int64Arg(args, "after_message"); ok && id > 0 {
		return fmt.Sprintf("local_id:%d", id)
	}
	if after := getStr(args, "after"); after != "" {
		return "time:" + after
	}
	return ""
}

func newestReadEventsCursor(events []map[string]any) string {
	for i := len(events) - 1; i >= 0; i-- {
		if cursor := rowString(wcdb.Row(events[i]), "cursor"); cursor != "" {
			return cursor
		}
	}
	return ""
}

func readEventsSessionCursor(a map[string]any) (sessionReadEventsCursor, bool, error) {
	if raw := getStr(a, "cursor"); raw != "" {
		cursor, err := parseSessionReadEventsCursor(raw)
		return cursor, true, err
	}
	if s := firstNonEmpty(getStr(a, "since_time"), getStr(a, "since"), getStr(a, "after")); s != "" {
		ts, err := parseTS(s)
		return sessionReadEventsCursor{Timestamp: ts}, true, err
	}
	return sessionReadEventsCursor{}, false, nil
}

func parseSessionReadEventsCursor(raw string) (sessionReadEventsCursor, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "session:") {
		return sessionReadEventsCursor{}, fmt.Errorf("invalid session cursor %q", raw)
	}
	parts := strings.SplitN(strings.TrimPrefix(raw, "session:"), ":", 2)
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || ts < 0 {
		return sessionReadEventsCursor{}, fmt.Errorf("invalid session cursor %q", raw)
	}
	cursor := sessionReadEventsCursor{Timestamp: ts}
	if len(parts) == 1 || parts[1] == "" {
		return cursor, nil
	}
	username, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return sessionReadEventsCursor{}, fmt.Errorf("invalid session cursor %q", raw)
	}
	cursor.Username = string(username)
	return cursor, nil

}

func encodeSessionReadEventsCursor(cursor sessionReadEventsCursor) string {
	if cursor.Timestamp <= 0 {
		return ""
	}
	if cursor.Username == "" {
		return fmt.Sprintf("session:%d", cursor.Timestamp)
	}
	return fmt.Sprintf("session:%d:%s", cursor.Timestamp,
		base64.RawURLEncoding.EncodeToString([]byte(cursor.Username)))
}

func mapStringAny(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func readEventsPollInterval(a map[string]any) time.Duration {
	raw := firstNonEmpty(getStr(a, "poll_interval"), getStr(a, "interval"))
	if raw == "" {
		return 2 * time.Second
	}
	if n, ok, _ := int64Arg(a, "poll_interval"); ok && n > 0 {
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 2 * time.Second
	}
	if d < 500*time.Millisecond {
		return 500 * time.Millisecond
	}
	return d
}
