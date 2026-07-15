package main

import (
	"testing"

	"github.com/r266-tech/wechat-cli/internal/wcdb"
)

func TestDisplayAndCursorUseSameStableMessageOrder(t *testing.T) {
	rows := []wcdb.Row{
		{"local_id": int64(1), "sort_seq": int64(300), "create_time": int64(100)},
		{"local_id": int64(2), "sort_seq": int64(100), "create_time": int64(300)},
		{"local_id": int64(3), "sort_seq": int64(200), "create_time": int64(200)},
	}
	cursor := messageCursorMeta(rows)
	if cursor["oldest_local_id"] != int64(2) || cursor["newest_local_id"] != int64(1) {
		t.Fatalf("cursor = %#v", cursor)
	}
	applyMessageDisplayOrder(rows, "asc")
	if got := []int64{rowInt64(rows[0], "local_id"), rowInt64(rows[1], "local_id"), rowInt64(rows[2], "local_id")}; got[0] != 2 || got[1] != 3 || got[2] != 1 {
		t.Fatalf("display order = %v, want [2 3 1]", got)
	}
}

func TestTimelineEnvelopeSurfacesPartialMediaEnrichment(t *testing.T) {
	page := messagePageInfo{
		Warnings:              []string{"media_enrichment_failed: test"},
		MediaEnrichmentFailed: true,
	}
	env := messageTimelineEnvelope(map[string]any{"chat": "test"}, nil, []map[string]any{}, page, "sort_seq DESC, local_id DESC", "asc")
	warnings, _ := env["warnings"].([]string)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v", env["warnings"])
	}
	freshness, _ := env["freshness"].(map[string]any)
	if freshness["media_enrichment_complete"] != false {
		t.Fatalf("freshness = %#v", freshness)
	}
}

func TestTimelineShardWarningDoesNotMisreportMediaEnrichment(t *testing.T) {
	page := messagePageInfo{Warnings: []string{"message_shard_unavailable: test"}}
	env := messageTimelineEnvelope(map[string]any{"chat": "test"}, nil, []map[string]any{}, page, "sort_seq DESC, local_id DESC", "asc")
	freshness, _ := env["freshness"].(map[string]any)
	if freshness["media_enrichment_complete"] != true {
		t.Fatalf("freshness = %#v", freshness)
	}
}
