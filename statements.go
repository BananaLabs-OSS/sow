package sew

import "strings"

// statements.go — split a migration body into individual SQL statements on top-level
// semicolons, so each runs as its own Exec. The scanner respects single-quoted strings,
// double-quoted identifiers, and -- / /* */ comments, so a ';' inside a string or comment
// does not split. This covers ordinary DDL/DML; a statement containing a bare ';' as part
// of its own body (e.g. a trigger with BEGIN…; …; END) should be authored as one file
// where that whole block is the only statement, or split across migrations.

func splitStatements(body string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}

	rs := []rune(body)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch c {
		case '\'', '"': // quoted string / identifier — copy verbatim to the closing quote
			quote := c
			cur.WriteRune(c)
			for i++; i < len(rs); i++ {
				cur.WriteRune(rs[i])
				if rs[i] == quote {
					// A doubled quote ('') is an escaped quote, not a terminator.
					if i+1 < len(rs) && rs[i+1] == quote {
						i++
						cur.WriteRune(rs[i])
						continue
					}
					break
				}
			}
		case '-': // line comment?  "--" to end of line
			if i+1 < len(rs) && rs[i+1] == '-' {
				for i < len(rs) && rs[i] != '\n' {
					cur.WriteRune(rs[i])
					i++
				}
				if i < len(rs) {
					cur.WriteRune(rs[i]) // the newline
				}
			} else {
				cur.WriteRune(c)
			}
		case '/': // block comment?  "/* ... */"
			if i+1 < len(rs) && rs[i+1] == '*' {
				cur.WriteString("/*")
				i += 2
				for i < len(rs) && !(rs[i] == '*' && i+1 < len(rs) && rs[i+1] == '/') {
					cur.WriteRune(rs[i])
					i++
				}
				if i+1 < len(rs) {
					cur.WriteString("*/")
					i++
				}
			} else {
				cur.WriteRune(c)
			}
		case ';': // top-level statement terminator
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	return out
}

// firstWords returns a short prefix of a statement for error messages (single line,
// clipped), so a failing migration points at the offending statement without dumping it.
func firstWords(stmt string) string {
	stmt = strings.TrimSpace(strings.ReplaceAll(stmt, "\n", " "))
	for strings.Contains(stmt, "  ") {
		stmt = strings.ReplaceAll(stmt, "  ", " ")
	}
	if len(stmt) > 60 {
		return stmt[:60] + "…"
	}
	return stmt
}
