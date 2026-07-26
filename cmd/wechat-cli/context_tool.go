package main

import (
	"fmt"
	"strconv"

	"github.com/r266-tech/wechat-cli/internal/wcdb"
	"github.com/r266-tech/wechat-cli/internal/wxkind"
)

type contextAnchorRef struct {
	LocalID  int64
	ServerID int64
	Kind     string
}

func (s *server) toolMessageContext(a map[string]any) (any, error) {
	talker, err := s.resolveLooseChatArg(a)
	if err != nil {
		return nil, err
	}
	if talker == "" {
		return nil, fmt.Errorf("talker or chat is required")
	}
	if aggregatorSessions[talker] {
		return nil, fmt.Errorf("%q is an aggregator session; pass the concrete gh_*/wxid talker instead", talker)
	}
	ref, err := contextAnchorRefFromArgs(a)
	if err != nil {
		return nil, err
	}
	beforeCount := contextCountArg(a, "before_count", "before_messages", 20)
	afterCount := contextCountArg(a, "after_count", "after_messages", 20)
	includeAnchor := getBoolDefault(a, "include_anchor", true)

	tableName := "Msg_" + talkerHash(talker)
	shards, err := s.findMsgDBs(tableName)
	if err != nil {
		return nil, err
	}
	defer closeMsgDBs(shards)
	warnings := msgShardWarnings(shards)
	mediaEnrichmentFailed := false

	anchor, err := s.findContextAnchorRow(shards, tableName, ref)
	if err != nil {
		return nil, err
	}
	older, err := s.contextWindowRows(shards, tableName, anchor, "before", beforeCount)
	if err != nil {
		return nil, err
	}
	newer, err := s.contextWindowRows(shards, tableName, anchor, "after", afterCount)
	if err != nil {
		return nil, err
	}

	sortLiveMessageRows(older, "sort_seq ASC, local_id ASC")
	sortLiveMessageRows(newer, "sort_seq ASC, local_id ASC")
	rows := make([]wcdb.Row, 0, len(older)+len(newer)+1)
	rows = append(rows, older...)
	if includeAnchor {
		anchor["context_role"] = "anchor"
		rows = append(rows, anchor)
	}
	rows = append(rows, newer...)
	for _, r := range rows {
		if rowInt64(r, "local_id") == rowInt64(anchor, "local_id") && rowInt64(r, "server_id") == rowInt64(anchor, "server_id") {
			r["context_role"] = "anchor"
		} else if contextRowBeforeAnchor(r, anchor) {
			r["context_role"] = "before"
		} else {
			r["context_role"] = "after"
		}
	}
	if getStr(a, "display_order") == "desc" {
		sortLiveMessageRows(rows, "sort_seq DESC, local_id DESC")
	}
	allRows := append([]wcdb.Row{anchor}, rows...)
	setContextTalker(talker, allRows)
	if includeMediaPathsForMessages(a) {
		if err := s.enrichMessageMediaResources(rows); err != nil {
			mediaEnrichmentFailed = true
			warnings = appendUniqueStrings(warnings, "media_enrichment_failed: "+err.Error())
		}
	}
	s.finishContextRows(talker, allRows)
	messages := agentMessages(rows, includeDebugOutput(a))
	attachContextRoles(messages, rows)
	return map[string]any{
		"query": compactMap(map[string]any{
			"chat":           getStr(a, "chat"),
			"talker":         talker,
			"display_name":   rowString(anchor, "talker_display_name"),
			"anchor":         agentMessageID(anchor),
			"before_count":   beforeCount,
			"after_count":    afterCount,
			"include_anchor": includeAnchor,
			"display_order":  firstNonEmpty(getStr(a, "display_order"), "asc"),
			"returned":       len(messages),
		}),
		"freshness": compactMap(map[string]any{
			"message_source":            "live_message_db",
			"metadata_cache_role":       "chat/sender display names only",
			"anchor_time":               agentMessageTime(anchor),
			"anchor_time_iso":           agentMessageTimeISO(anchor),
			"media_enrichment_complete": !mediaEnrichmentFailed,
		}),
		"warnings": warnings,
		"messages": messages,
	}, nil
}

func contextAnchorRefFromArgs(a map[string]any) (contextAnchorRef, error) {
	ref, ok, err := contextAnchorRefFromKeys(a,
		[]string{"local_id", "message_local_id", "around_local_id"},
		[]string{"server_id_str", "message_server_id_str", "around_server_id_str", "server_id", "message_server_id", "around_server_id"})
	if ok || err != nil {
		return ref, err
	}
	return contextAnchorRef{}, fmt.Errorf("local_id or server_id is required")
}

func contextAnchorRefFromKeys(a map[string]any, localKeys, serverKeys []string) (contextAnchorRef, bool, error) {
	for _, key := range localKeys {
		if n, ok, err := int64Arg(a, key); ok || err != nil {
			if err == nil && n <= 0 {
				err = fmt.Errorf("invalid argument %q: expected positive message id, got %d", key, n)
			}
			return contextAnchorRef{LocalID: n, Kind: key}, ok, err
		}
	}
	for _, key := range serverKeys {
		if n, ok, err := int64Arg(a, key); ok || err != nil {
			if err == nil && n <= 0 {
				err = fmt.Errorf("invalid argument %q: expected positive message id, got %d", key, n)
			}
			return contextAnchorRef{ServerID: n, Kind: key}, ok, err
		}
	}
	return contextAnchorRef{}, false, nil
}

func contextCountArg(a map[string]any, primary, alias string, def int) int {
	n := getInt(a, primary, -1)
	if n < 0 {
		n = getInt(a, alias, -1)
	}
	if n < 0 {
		n = getInt(a, "limit", def)
	}
	if n < 0 {
		n = def
	}
	if n > 500 {
		return 500
	}
	return n
}

func messageBoundaryRefFromArgs(a map[string]any, direction string) (contextAnchorRef, bool, error) {
	switch direction {
	case "before":
		return contextAnchorRefFromKeys(a,
			[]string{"before_message", "before_message_local_id", "before_local_id", "before_message_id"},
			[]string{"before_server_id_str", "before_message_server_id_str", "before_server_id", "before_message_server_id"})
	case "after":
		return contextAnchorRefFromKeys(a,
			[]string{"after_message", "after_message_local_id", "after_local_id", "after_message_id", "since_local_id", "since_message"},
			[]string{"after_server_id_str", "after_message_server_id_str", "after_server_id", "after_message_server_id"})
	default:
		return contextAnchorRef{}, false, fmt.Errorf("invalid message boundary direction %q", direction)
	}
}

func messageBoundaryQueryValue(a map[string]any, direction string) string {
	ref, ok, err := messageBoundaryRefFromArgs(a, direction)
	if !ok || err != nil {
		return ""
	}
	if ref.LocalID > 0 {
		return fmt.Sprintf("local_id:%d", ref.LocalID)
	}
	if ref.ServerID > 0 {
		return fmt.Sprintf("server_id:%d", ref.ServerID)
	}
	return ""
}

func messageBoundaryWhere(anchor wcdb.Row, direction string) (string, []any, error) {
	sortSeq := rowInt64(anchor, "sort_seq")
	localID := rowInt64(anchor, "local_id")
	switch direction {
	case "before":
		return "(sort_seq < ? OR (sort_seq = ? AND local_id < ?))", []any{sortSeq, sortSeq, localID}, nil
	case "after":
		return "(sort_seq > ? OR (sort_seq = ? AND local_id > ?))", []any{sortSeq, sortSeq, localID}, nil
	default:
		return "", nil, fmt.Errorf("invalid message boundary direction %q", direction)
	}
}

func (s *server) appendMessageBoundaryWhere(a map[string]any, shards []msgShardDB, tableName string, where *[]string, args *[]any) error {
	for _, direction := range []string{"before", "after"} {
		ref, ok, err := messageBoundaryRefFromArgs(a, direction)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		anchor, err := s.findContextAnchorRow(shards, tableName, ref)
		if err != nil {
			return fmt.Errorf("%s_message anchor: %w", direction, err)
		}
		wc, wcArgs, err := messageBoundaryWhere(anchor, direction)
		if err != nil {
			return err
		}
		*where = append(*where, wc)
		*args = append(*args, wcArgs...)
	}
	return nil
}

func (s *server) findContextAnchorRow(shards []msgShardDB, tableName string, ref contextAnchorRef) (wcdb.Row, error) {
	var where string
	var args []any
	if ref.LocalID != 0 {
		where = "local_id = ?"
		args = append(args, ref.LocalID)
	} else {
		where = "server_id = ?"
		args = append(args, ref.ServerID)
	}
	rows, err := s.queryContextRows(shards, tableName, where, args, "sort_seq DESC, local_id DESC", 10)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("anchor message not found for %s", ref.Kind)
	}
	sortLiveMessageRows(rows, "sort_seq DESC, local_id DESC")
	return rows[0], nil
}

func (s *server) contextWindowRows(shards []msgShardDB, tableName string, anchor wcdb.Row, direction string, limit int) ([]wcdb.Row, error) {
	if limit <= 0 {
		return nil, nil
	}
	var where, order string
	var args []any
	var err error
	switch direction {
	case "before":
		order = "sort_seq DESC, local_id DESC"
	case "after":
		order = "sort_seq ASC, local_id ASC"
	default:
		return nil, fmt.Errorf("invalid context direction %q", direction)
	}
	where, args, err = messageBoundaryWhere(anchor, direction)
	if err != nil {
		return nil, err
	}
	rows, err := s.queryContextRows(shards, tableName, where, args, order, limit)
	if err != nil {
		return nil, err
	}
	sortLiveMessageRows(rows, order)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (s *server) queryContextRows(shards []msgShardDB, tableName, where string, args []any, order string, limit int) ([]wcdb.Row, error) {
	var rows []wcdb.Row
	shardLimit := limit
	if shardLimit <= 0 {
		shardLimit = 1
	}
	for _, shard := range shards {
		qargs := append([]any(nil), args...)
		qargs = append(qargs, shardLimit)
		shardRows, err := shard.DB.Query(fmt.Sprintf(`SELECT local_id, server_id, local_type, sort_seq,
			real_sender_id, create_time, status, message_content, source
			FROM %s WHERE %s
			ORDER BY %s LIMIT ?`, quoteIdent(tableName), where, order), qargs...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", shard.Name, err)
		}
		n2i, _ := loadName2Id(shard.DB)
		if n2i != nil {
			shardRows = resolveSenders(shardRows, n2i)
		}
		rows = append(rows, shardRows...)
	}
	return prepareContextRows(rows), nil
}

func prepareContextRows(rows []wcdb.Row) []wcdb.Row {
	rows = enrichMessages(decodeFields(rows, "message_content", "source"))
	return rows
}

func (s *server) finishContextRows(talker string, rows []wcdb.Row) {
	if len(rows) == 0 {
		return
	}
	setContextTalker(talker, rows)
	s.attachMessageDisplayNames(rows)
	if selfWxid := s.selfWxid(); selfWxid != "" {
		for _, r := range rows {
			sw := rowString(r, "sender_wxid")
			if sw != "" {
				r["is_from_me"] = (sw == selfWxid)
			}
		}
	}
	for _, r := range rows {
		if sid := rowInt64(r, "server_id"); sid != 0 {
			r["server_id_str"] = strconv.FormatInt(sid, 10)
		}
		delete(r, "real_sender_id")
		delete(r, "status")
		delete(r, "source")
		delete(r, "local_type")
		delete(r, "sort_seq")
	}
}

func setContextTalker(talker string, rows []wcdb.Row) {
	for _, r := range rows {
		if rowString(r, "talker") == "" && talker != "" {
			r["talker"] = talker
		}
		if rowString(r, "chat_type") == "" && talker != "" {
			r["chat_type"] = agentChatType(talker, wxkind.ClassifyUsername(talker), false)
		}
	}
}

func contextRowBeforeAnchor(row, anchor wcdb.Row) bool {
	rs := rowInt64(row, "sort_seq")
	as := rowInt64(anchor, "sort_seq")
	if rs != as {
		return rs < as
	}
	return rowInt64(row, "local_id") < rowInt64(anchor, "local_id")
}

func attachContextRoles(messages []map[string]any, rows []wcdb.Row) {
	roleByID := map[int64]string{}
	for _, r := range rows {
		if role := rowString(r, "context_role"); role != "" {
			roleByID[rowInt64(r, "local_id")] = role
		}
	}
	for _, msg := range messages {
		id, _ := msg["id"].(map[string]any)
		if id == nil {
			continue
		}
		localID, _ := integerArgValue(id["local_id"])
		if role := roleByID[localID]; role != "" {
			msg["context_role"] = role
		}
	}
}
