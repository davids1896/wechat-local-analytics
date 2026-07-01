package wcdb

import (
	"errors"
	"testing"
)

func TestQuoteIdentEscapesDoubleQuotes(t *testing.T) {
	got := quoteIdent(`weird"name`)
	if got != `"weird""name"` {
		t.Fatalf("quoteIdent mismatch: %q", got)
	}
}

func TestSQLStringEscapesSingleQuotes(t *testing.T) {
	got := sqlString(`Bob's chat`)
	if got != `'Bob''s chat'` {
		t.Fatalf("sqlString mismatch: %q", got)
	}
}

func TestIsSkippableSchemaErrorOnlySkipsVirtualTableTokenizerIssues(t *testing.T) {
	if !isSkippableSchemaError(
		`CREATE VIRTUAL TABLE msg USING fts5(content, tokenize='mmicu')`,
		errors.New("no such tokenizer: mmicu"),
	) {
		t.Fatalf("expected tokenizer virtual-table error to be skippable")
	}
	if isSkippableSchemaError(
		`CREATE TABLE msg(id INTEGER PRIMARY KEY)`,
		errors.New("syntax error"),
	) {
		t.Fatalf("plain table syntax errors must not be skippable")
	}
	if isSkippableSchemaError(`CREATE VIRTUAL TABLE msg USING fts5(content)`, nil) {
		t.Fatalf("nil errors must not be skippable")
	}
}
