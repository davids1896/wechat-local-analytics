package main

import (
	"fmt"
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
	args := copyToolArgs(a)
	if args["limit"] == nil {
		args["limit"] = int64(50)
	}
	if v := firstNonEmpty(getStr(args, "since_time"), getStr(args, "since")); v != "" && args["after"] == nil {
		args["after"] = v
	}
	if id, ok, err := int64Arg(args, "since_local_id"); err != nil {
		return nil, err
	} else if ok && args["after_message"] == nil {
		args["after_message"] = id
	}
	if cursor := getStr(args, "cursor"); cursor != "" && args["after_message"] == nil && args["after"] == nil {
		applyReadEventsCursor(args, cursor)
	}
	args["order"] = "desc"
	args["display_order"] = "asc"

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
	return map[string]any{
		"query": compactMap(map[string]any{
			"mode":     "messages",
			"chat":     firstNonEmpty(getStr(a, "chat"), getStr(a, "talker")),
			"from_me":  queryFromMeArg(a),
			"limit":    getInt(args, "limit", 50),
			"cursor":   getStr(a, "cursor"),
			"returned": len(events),
		}),
		"freshness": env["freshness"],
		"cursor":    newestReadEventsCursor(events),
		"events":    events,
	}, nil
}

func (s *server) readSessionEvents(a map[string]any) (any, error) {
	limit := getInt(a, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	scanLimit := getInt(a, "scan_limit", limit)
	if scanLimit < limit {
		scanLimit = limit
	}
	args := copyToolArgs(a)
	args["limit"] = int64(scanLimit)

	raw, err := s.toolSessions(args)
	if err != nil {
		return nil, err
	}
	rows, ok := cliResultRows(raw)
	if !ok {
		return nil, fmt.Errorf("read_events internal error: sessions returned %T", raw)
	}
	sinceTS, err := readEventsSinceTimestamp(a)
	if err != nil {
		return nil, err
	}
	events := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		ts := firstNonZeroInt64(int64MapValue(row, "last_timestamp"), int64MapValue(row, "sort_timestamp"))
		if sinceTS > 0 && ts <= sinceTS {
			continue
		}
		event := compactMap(map[string]any{
			"type":       "session",
			"event_time": cliFormatUnixISO(ts),
			"cursor":     fmt.Sprintf("session:%d", ts),
			"session":    row,
		})
		events = append(events, event)
		if len(events) >= limit {
			break
		}
	}
	reverseReadEvents(events)
	return map[string]any{
		"query": compactMap(map[string]any{
			"mode":       "sessions",
			"limit":      limit,
			"scan_limit": scanLimit,
			"cursor":     getStr(a, "cursor"),
			"returned":   len(events),
		}),
		"freshness": map[string]any{
			"message_source":      "metadata_cache_sessions",
			"metadata_cache_role": "session/unread observation",
		},
		"cursor": newestReadEventsCursor(events),
		"events": events,
	}, nil
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

func newestReadEventsCursor(events []map[string]any) string {
	for i := len(events) - 1; i >= 0; i-- {
		if cursor := rowString(wcdb.Row(events[i]), "cursor"); cursor != "" {
			return cursor
		}
	}
	return ""
}

func readEventsSinceTimestamp(a map[string]any) (int64, error) {
	if cursor := getStr(a, "cursor"); strings.HasPrefix(cursor, "session:") {
		return strconv.ParseInt(strings.TrimPrefix(cursor, "session:"), 10, 64)
	}
	if s := firstNonEmpty(getStr(a, "since_time"), getStr(a, "since"), getStr(a, "after")); s != "" {
		return parseTS(s)
	}
	return 0, nil
}

func reverseReadEvents(events []map[string]any) {
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
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
