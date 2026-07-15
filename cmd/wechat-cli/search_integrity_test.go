package main

import (
	"strings"
	"testing"

	"github.com/r266-tech/wechat-cli/internal/wcdb"
)

func TestEscapeSQLLikeLiteralTreatsWildcardsLiterally(t *testing.T) {
	if got, want := escapeSQLLikeLiteral(`50%_off\today`), `50\%\_off\\today`; got != want {
		t.Fatalf("escapeSQLLikeLiteral = %q, want %q", got, want)
	}
}

func TestSearchFTSQueryUsesDiscoveredTablesAndStableOrder(t *testing.T) {
	query := searchFTSQuery([]string{"message_fts_v4_0_content", "message_fts_v4_7_content"}, `c0 LIKE ? ESCAPE '\'`)
	for _, table := range []string{"message_fts_v4_0_content", "message_fts_v4_7_content"} {
		if !strings.Contains(query, `"`+table+`"`) {
			t.Fatalf("query missing %s: %s", table, query)
		}
	}
	if !strings.Contains(query, "ORDER BY create_time DESC, session_id DESC, local_id DESC") {
		t.Fatalf("query has unstable order: %s", query)
	}
}

func TestFTSContentTableNameValidation(t *testing.T) {
	for _, valid := range []string{"message_fts_v4_0_content", "message_fts_v4_12_content"} {
		if !ftsContentTableRE.MatchString(valid) {
			t.Fatalf("valid table rejected: %s", valid)
		}
	}
	for _, invalid := range []string{"message_fts_v4_x_content", "message_fts_v4_0_content;DROP TABLE x", "other"} {
		if ftsContentTableRE.MatchString(invalid) {
			t.Fatalf("invalid table accepted: %s", invalid)
		}
	}
}

func TestSearchWithContextPreservesIncompleteSearchDiagnostics(t *testing.T) {
	freshness, warnings := searchWithContextDiagnostics(cliRowsResult{
		Rows: []wcdb.Row{{"local_id": int64(1)}},
		Freshness: map[string]any{
			"message_source": "live_message_fts_db",
			"complete":       false,
		},
		Warnings: []string{"search_scan_truncated_after_50000_rows"},
	})
	if freshness["complete"] != false {
		t.Fatalf("search-context freshness lost incomplete state: %#v", freshness)
	}
	if freshness["search_source"] != "live_message_fts_db" {
		t.Fatalf("search-context freshness lost search source: %#v", freshness)
	}
	if len(warnings) != 1 || warnings[0] != "search_scan_truncated_after_50000_rows" {
		t.Fatalf("search-context warnings lost source diagnostics: %#v", warnings)
	}
}
