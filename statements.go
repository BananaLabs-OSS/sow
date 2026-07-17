package sow

import "strings"

// statements.go — split a migration body into individual SQL statements on top-level
// semicolons, so each runs as its own Exec. The scanner respects single-quoted strings,
// double-quoted identifiers, and -- / /* */ comments, so a ';' inside a string or comment
// does not split. Postgres dollar-quoted bodies ($$…$$, $tag$…$tag$) are copied
// verbatim, so a function, trigger or DO block whose body contains ';' stays a single
// statement. A '$' that does not open a well-formed tag — a $1 placeholder, say — is an
// ordinary character.

// isIdentRune reports whether r can appear inside a PostgreSQL identifier. A dollar
// quote cannot open immediately after one, because '$' is itself a legal identifier
// character: in 'AS$$' Postgres lexes the whole thing as the identifier 'as$$'.
func isIdentRune(r rune) bool {
	return r == '_' || r == '$' || r >= 0x80 ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// dollarTag returns the dollar-quote delimiter opening at rs[i] — "$$" or "$tag$" —
// where tag is [A-Za-z_][A-Za-z0-9_]*. It reports false for anything else, notably the
// $1/$2 placeholders in Postgres SQL, which must never open a quoted body.
func dollarTag(rs []rune, i int) (string, bool) {
	if i >= len(rs) || rs[i] != '$' {
		return "", false
	}
	j := i + 1
	for j < len(rs) {
		r := rs[j]
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if j > i+1 {
			ok = ok || (r >= '0' && r <= '9')
		}
		if !ok {
			break
		}
		j++
	}
	if j < len(rs) && rs[j] == '$' {
		return string(rs[i : j+1]), true
	}
	return "", false
}

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
		case '$': // Postgres dollar-quoted body — opaque through the matching tag
			tag, ok := dollarTag(rs, i)
			if !ok || (i > 0 && isIdentRune(rs[i-1])) {
				cur.WriteRune(c)
				break
			}
			n := len([]rune(tag))
			cur.WriteString(tag)
			// Copy verbatim to the matching closing tag. Only the SAME tag closes:
			// $a$ … $b$ … $a$ ends on the second $a$. Quotes, comments and ';' inside
			// are literal. An unterminated body runs to EOF, which is what Postgres
			// itself reports as an unterminated dollar-quoted string.
			for i += n; i < len(rs); i++ {
				if t, ok := dollarTag(rs, i); ok && t == tag {
					cur.WriteString(tag)
					i += n - 1
					break
				}
				cur.WriteRune(rs[i])
			}
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
