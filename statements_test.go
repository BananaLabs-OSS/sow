package sow

import (
	"strings"
	"testing"
)

// statements_test.go — Postgres dollar-quoted strings ($$…$$ / $tag$…$tag$). Inside a
// dollar-quoted body, ';' is literal text and ordinary quoting rules are suspended, so
// the splitter must copy the whole body verbatim to the matching closing tag.

func TestSplitStatementsDollarQuoted(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "unnamed dollar quote hides semicolons",
			body: "CREATE FUNCTION f() RETURNS int AS $$ BEGIN a; b; RETURN 1; END $$ LANGUAGE plpgsql;\nSELECT 1;",
			want: []string{
				"CREATE FUNCTION f() RETURNS int AS $$ BEGIN a; b; RETURN 1; END $$ LANGUAGE plpgsql",
				"SELECT 1",
			},
		},
		{
			name: "tagged dollar quote hides semicolons",
			body: "CREATE FUNCTION g() RETURNS void AS $fn$ BEGIN x; y; END $fn$ LANGUAGE plpgsql;\nSELECT 2;",
			want: []string{
				"CREATE FUNCTION g() RETURNS void AS $fn$ BEGIN x; y; END $fn$ LANGUAGE plpgsql",
				"SELECT 2",
			},
		},
		{
			name: "a differently tagged $$ inside a $tag$ body is literal text",
			body: "CREATE FUNCTION h() RETURNS text AS $outer$ SELECT $$ inner; text $$ ; $outer$ LANGUAGE sql;\nSELECT 3;",
			want: []string{
				"CREATE FUNCTION h() RETURNS text AS $outer$ SELECT $$ inner; text $$ ; $outer$ LANGUAGE sql",
				"SELECT 3",
			},
		},
		{
			name: "unbalanced single quote inside a dollar-quoted body does not swallow the rest",
			body: "CREATE FUNCTION i() RETURNS text AS $q$ it's a ; body $q$ LANGUAGE sql;\nSELECT 4;",
			want: []string{
				"CREATE FUNCTION i() RETURNS text AS $q$ it's a ; body $q$ LANGUAGE sql",
				"SELECT 4",
			},
		},
		{
			name: "comment markers inside a dollar-quoted body are literal text",
			body: "CREATE FUNCTION j() RETURNS text AS $c$ -- not a comment; /* nor this; $c$ LANGUAGE sql;\nSELECT 5;",
			want: []string{
				"CREATE FUNCTION j() RETURNS text AS $c$ -- not a comment; /* nor this; $c$ LANGUAGE sql",
				"SELECT 5",
			},
		},
		{
			name: "dollar placeholders are not dollar quotes",
			body: "SELECT * FROM f($1, $2);\nSELECT 6;",
			want: []string{"SELECT * FROM f($1, $2)", "SELECT 6"},
		},
		{
			name: "bare dollar amid identifiers is not a dollar quote",
			body: "SELECT a$b FROM t;\nSELECT 7;",
			want: []string{"SELECT a$b FROM t", "SELECT 7"},
		},
		{
			name: "dollar quote inside a single-quoted literal is literal text",
			body: "INSERT INTO t VALUES ('$$ not a quote; still one statement');\nSELECT 8;",
			want: []string{
				"INSERT INTO t VALUES ('$$ not a quote; still one statement')",
				"SELECT 8",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitStatements(tt.body)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d statements, want %d:\n got: %#v\nwant: %#v", len(got), len(tt.want), got, tt.want)
			}
			for i := range tt.want {
				if strings.TrimSpace(got[i]) != tt.want[i] {
					t.Errorf("statement %d:\n got: %q\nwant: %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// An unterminated dollar quote must not lose the text after it: the remainder is one
// statement, not silently dropped or split on its embedded semicolons.
func TestSplitStatementsUnterminatedDollarQuote(t *testing.T) {
	got := splitStatements("SELECT 1;\nCREATE FUNCTION k() AS $$ BEGIN oops; END")
	if len(got) != 2 {
		t.Fatalf("got %d statements, want 2: %#v", len(got), got)
	}
	if !strings.Contains(got[1], "oops;") {
		t.Errorf("unterminated body was mangled: %q", got[1])
	}
}
