package main

import (
	"fmt"

	"github.com/r266-tech/wechat-cli/internal/wcdb"
)

func (s *server) toolSearchWithContext(a map[string]any) (any, error) {
	raw, err := s.toolSearch(a)
	if err != nil {
		return nil, err
	}
	rows, ok := cliResultRows(raw)
	if !ok {
		return nil, fmt.Errorf("search_with_context internal error: search returned %T", raw)
	}
	freshness, warnings := searchWithContextDiagnostics(raw)
	searchLimit := getInt(a, "limit", 20)
	if searchLimit > 0 && len(rows) > searchLimit {
		rows = rows[:searchLimit]
	}
	contextLimit := getInt(a, "context_limit", minInt(searchLimit, 5))
	if contextLimit < 0 {
		contextLimit = 0
	}
	if contextLimit > 20 {
		contextLimit = 20
	}
	beforeCount := searchContextCountArg(a, "before_count", "before_messages", 5)
	afterCount := searchContextCountArg(a, "after_count", "after_messages", 5)
	queryBeforeCount := beforeCount
	queryAfterCount := afterCount
	if contextLimit == 0 {
		queryBeforeCount = 0
		queryAfterCount = 0
	}
	includeMedia := getBoolDefault(a, "include_media_paths", true)
	includeDebug := includeDebugOutput(a)

	hits := make([]map[string]any, 0, len(rows))
	contextsReturned := 0
	for i, rawRow := range rows {
		row := wcdb.Row(rawRow)
		msg := cliSearchMessageRow(rawRow, a)
		msg["context_role"] = "search_hit"
		hit := map[string]any{
			"anchor_id": msg["id"],
			"message":   msg,
		}
		if i < contextLimit {
			talker := rowString(row, "talker")
			localID := rowInt64(row, "local_id")
			if talker == "" || localID <= 0 {
				hit["context_error"] = "search hit has no expandable talker/local_id"
			} else {
				ctxArgs := map[string]any{
					"talker":              talker,
					"local_id":            localID,
					"before_count":        beforeCount,
					"after_count":         afterCount,
					"include_anchor":      true,
					"include_media_paths": includeMedia,
					"include_debug":       includeDebug,
					"display_order":       "asc",
				}
				ctx, err := s.toolMessageContext(ctxArgs)
				if err != nil {
					hit["context_error"] = err.Error()
				} else {
					if ctxMap := mapStringAny(ctx); len(ctxMap) > 0 {
						applyAgentTextOutputOptions(mapSliceAny(ctxMap["messages"]), a)
						ctx = ctxMap
					}
					hit["context"] = ctx
					contextsReturned++
				}
			}
		}
		hits = append(hits, hit)
	}
	query := compactMap(map[string]any{
		"keyword":           getStr(a, "keyword"),
		"chat":              firstNonEmpty(getStr(a, "chat"), getStr(a, "talker")),
		"sender":            getStr(a, "sender"),
		"from_me":           queryFromMeArg(a),
		"type":              firstNonEmpty(getStr(a, "kind_name"), getStr(a, "type")),
		"after":             getStr(a, "after"),
		"before":            getStr(a, "before"),
		"limit":             searchLimit,
		"context_limit":     contextLimit,
		"before_count":      queryBeforeCount,
		"after_count":       queryAfterCount,
		"returned":          len(hits),
		"contexts_returned": contextsReturned,
	})
	if _, ok := a["context_limit"]; ok {
		query["context_limit"] = contextLimit
		query["contexts_returned"] = contextsReturned
	}
	if complete, ok := freshness["complete"].(bool); ok && !complete {
		query["has_more_unknown"] = true
	}
	result := map[string]any{
		"query":     query,
		"freshness": freshness,
		"hits":      hits,
	}
	if len(warnings) > 0 {
		result["warnings"] = warnings
	}
	return result, nil
}

func searchWithContextDiagnostics(searchResult any) (map[string]any, []string) {
	searchFreshness, warnings := cliRowsResultMetadata(searchResult)
	freshness := map[string]any{
		"message_source":      "live_message_fts_plus_live_message_db_context",
		"metadata_cache_role": "chat/sender display names only",
	}
	for key, value := range searchFreshness {
		if key == "message_source" {
			freshness["search_source"] = value
			continue
		}
		freshness[key] = value
	}
	return freshness, warnings
}

func searchContextCountArg(a map[string]any, primary, alias string, def int) int {
	n := getInt(a, primary, -1)
	if n < 0 {
		n = getInt(a, alias, -1)
	}
	if n < 0 {
		n = def
	}
	if n < 0 {
		n = 0
	}
	if n > 500 {
		return 500
	}
	return n
}
