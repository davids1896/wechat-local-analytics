package main

import (
	"fmt"
	"testing"

	"github.com/r266-tech/wechat-cli/internal/wcdb"
)

func TestCollectSessionPageScansPastFilteredPrefix(t *testing.T) {
	all := make([]wcdb.Row, 0, 2105)
	for i := 0; i < 2100; i++ {
		all = append(all, wcdb.Row{"username": fmt.Sprintf("gh_%04d", i)})
	}
	for i := 0; i < 5; i++ {
		all = append(all, wcdb.Row{"username": fmt.Sprintf("group-%d@chatroom", i)})
	}
	fetch := func(limit, offset int) ([]wcdb.Row, error) {
		if offset >= len(all) {
			return nil, nil
		}
		end := minInt(offset+limit, len(all))
		return append([]wcdb.Row(nil), all[offset:end]...), nil
	}

	rows, err := collectSessionPage(3, 1, "group", fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("returned %d rows, want caller-provided over-fetch limit", len(rows))
	}
	for i, row := range rows {
		want := fmt.Sprintf("group-%d@chatroom", i+1)
		if got := rowString(row, "username"); got != want {
			t.Fatalf("row %d username = %q, want %q", i, got, want)
		}
	}
}

func TestCollectSessionPageAppliesOffsetAndOverfetchWithoutFilter(t *testing.T) {
	all := []wcdb.Row{{"username": "a"}, {"username": "b"}, {"username": "c"}, {"username": "d"}}
	fetch := func(limit, offset int) ([]wcdb.Row, error) {
		end := minInt(offset+limit, len(all))
		return append([]wcdb.Row(nil), all[offset:end]...), nil
	}
	rows, err := collectSessionPage(3, 1, "all", fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rowString(rows[0], "username") != "b" || rowString(rows[2], "username") != "d" {
		t.Fatalf("rows = %#v", rows)
	}
}
