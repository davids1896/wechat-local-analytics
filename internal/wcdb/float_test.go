package wcdb

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadColumnFloat(t *testing.T) {
	oldType := sqlite3_column_type
	oldDouble := sqlite3_column_double
	t.Cleanup(func() {
		sqlite3_column_type = oldType
		sqlite3_column_double = oldDouble
	})
	sqlite3_column_type = func(uintptr, int32) int32 { return COL_FLOAT }
	sqlite3_column_double = func(uintptr, int32) float64 { return 1.5 }
	if got, ok := readColumn(1, 0).(float64); !ok || got != 1.5 {
		t.Fatalf("readColumn REAL = %#v (%T), want float64(1.5)", got, got)
	}
}

func TestQueryReadsRealColumn(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("repository test WCDB library is bundled for macOS")
	}
	lib, err := filepath.Abs(filepath.Join("..", "..", "lib", "libWCDB.dylib"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lib); err != nil {
		t.Skipf("WCDB test library unavailable: %v", err)
	}
	if err := Bootstrap(lib); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	db, err := OpenPlain(filepath.Join(t.TempDir(), "real.db"), true)
	if err != nil {
		t.Fatalf("OpenPlain: %v", err)
	}
	defer db.Close()
	if err := db.Exec("CREATE TABLE values_test (x REAL); INSERT INTO values_test(x) VALUES (1.5)"); err != nil {
		t.Fatalf("create REAL fixture: %v", err)
	}
	rows, err := db.Query("SELECT x FROM values_test")
	if err != nil {
		t.Fatalf("query REAL fixture: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("REAL row count = %d, want 1", len(rows))
	}
	if got, ok := rows[0]["x"].(float64); !ok || got != 1.5 {
		t.Fatalf("REAL value = %#v (%T), want float64(1.5)", rows[0]["x"], rows[0]["x"])
	}
}
