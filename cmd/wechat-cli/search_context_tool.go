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
	rows, ok := raw.([]wcdb.Row)
	if !ok {
		return nil, fmt.Errorf("search_with_context internal error: search returned %T", raw)
	}
	searchLimit := getInt(a, "limit", 20)
	contextLimit := getInt(a, "context_limit", minInt(searchLimit, 5))
	if contextLimit < 0 {
		contextLimit = 0
	}
	if contextLimit > 20 {
		contextLimit = 20
	}
	beforeCount := contextCountArg(a, "before_count", "before_messages", 5)
	afterCount := contextCountArg(a, "after_count", "after_messages", 5)
	includeMedia := getBoolDefault(a, "include_media_paths", true)
	includeDebug := includeDebugOutput(a)

	hits := make([]map[string]any, 0, len(rows))
	contextsReturned := 0
	for i, row := range rows {
		msg := cliSearchMessageRow(map[string]any(row))
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
					hit["context"] = ctx
					contextsReturned++
				}
			}
		}
		hits = append(hits, hit)
	}
	return map[string]any{
		"query": compactMap(map[string]any{
			"keyword":           getStr(a, "keyword"),
			"chat":              firstNonEmpty(getStr(a, "chat"), getStr(a, "talker")),
			"sender":            getStr(a, "sender"),
			"type":              firstNonEmpty(getStr(a, "kind_name"), getStr(a, "type")),
			"after":             getStr(a, "after"),
			"before":            getStr(a, "before"),
			"limit":             searchLimit,
			"context_limit":     contextLimit,
			"before_count":      beforeCount,
			"after_count":       afterCount,
			"returned":          len(hits),
			"contexts_returned": contextsReturned,
		}),
		"freshness": map[string]any{
			"message_source":      "live_message_fts_plus_live_message_db_context",
			"metadata_cache_role": "chat/sender display names only",
		},
		"hits": hits,
	}, nil
}
