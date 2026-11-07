package dbx

import "strings"

// What the previous token makes of the next word in quoteIdentifiers.
const (
	identCtxNone         = iota
	identCtxDot          // t.name
	identCtxType         // ::type, CAST(x AS type)
	identCtxCollate      // COLLATE name
	identCtxExtract      // EXTRACT
	identCtxExtractField // EXTRACT(field
	identCtxCast         // CAST
	identCtxRelation     // INTO, TABLE, REFERENCES, VIEW, ON, WITH, RECURSIVE: name (...) is a relation
	identCtxRoleKeyword  // CREATE/ALTER/DROP USER, USER MAPPING FOR USER
)

// quoteIdentifiers double-quotes the unquoted identifiers of the PostgreSQL
// statement sql that PostgreSQL and SQLite read differently:
//
//   - mixed-case names (userId): PostgreSQL folds unquoted names to lower
//     case, SQLite keeps the spelling. Quoting keeps the spelling, so a name
//     resolves the same way in every statement that writes it the same way.
//   - a bare user: a column in SQLite, CURRENT_USER in PostgreSQL. It becomes
//     "user" (a mixed-case spelling is kept).
//
// It doesn't touch string, bit and dollar-quoted literals, comments, quoted
// names, $N params, numbers, PostgreSQL keywords, function names (name
// followed by "(", except after INTO, TABLE, REFERENCES, VIEW, ON, WITH,
// RECURSIVE and AS), type names (after "::" and "CAST(x AS"), the COLLATE
// name, the EXTRACT field and the type of a typed literal (Type 'value').
// Lower-case and upper-case names stay unquoted.
//
// sql is returned as is (no allocation) when nothing needs quoting.
func quoteIdentifiers(sql string) string {
	var b strings.Builder
	changed := false
	last := 0 // sql[last:i] is not copied to b yet
	ctx := identCtxNone
	depth := 0
	var castDepths uint64 // bit d: the parenthesis at depth d is CAST(

	n := len(sql)
	for i := 0; i < n; {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			i++
			continue
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
			continue
		case c == '/' && i+1 < n && sql[i+1] == '*':
			i = skipBlockComment(sql, i)
			continue
		case c == '\'':
			i = skipQuoted(sql, i, false)
		case c == '"':
			i = skipQuoted(sql, i, false)
		case c == '$':
			i = skipDollar(sql, i)
		case isDigit(c) || (c == '.' && i+1 < n && isDigit(sql[i+1])):
			i = skipNumber(sql, i)
		case isIdentStart(c):
			j := i + 1
			for j < n && isIdentChar(sql[j]) {
				j++
			}
			word := sql[i:j]

			// E'..' (backslash escapes), B'..', X'..', N'..', U&'..', U&".."
			if len(word) == 1 && j < n {
				switch c {
				case 'e', 'E', 'b', 'B', 'x', 'X', 'n', 'N':
					if sql[j] == '\'' {
						i = skipQuoted(sql, j, c == 'e' || c == 'E')
						ctx = identCtxNone
						continue
					}
				case 'u', 'U':
					if sql[j] == '&' && j+1 < n && (sql[j+1] == '\'' || sql[j+1] == '"') {
						i = skipQuoted(sql, j+1, false)
						ctx = identCtxNone
						continue
					}
				}
			}

			if q := identQuote(word, ctx, nextNonSpace(sql, j)); q != "" {
				if !changed {
					changed = true
					b.Grow(n + 16)
				}
				b.WriteString(sql[last:i])
				b.WriteByte('"')
				b.WriteString(q)
				b.WriteByte('"')
				last = j
			}
			ctx = identWordCtx(word, ctx, depth < 64 && castDepths&(1<<depth) != 0)
			i = j
			continue
		case c == '.':
			i++
			ctx = identCtxDot
			continue
		case c == ':' && i+1 < n && sql[i+1] == ':':
			i += 2
			ctx = identCtxType
			continue
		case c == '(':
			i++
			depth++
			if ctx == identCtxCast && depth < 64 {
				castDepths |= 1 << depth
			}
			if ctx == identCtxExtract {
				ctx = identCtxExtractField
			} else {
				ctx = identCtxNone
			}
			continue
		case c == ')':
			i++
			if depth > 0 {
				if depth < 64 {
					castDepths &^= 1 << depth
				}
				depth--
			}
		default:
			i++
		}
		ctx = identCtxNone
	}

	if !changed {
		return sql
	}
	b.WriteString(sql[last:])
	return b.String()
}

// identQuote returns the name to write double-quoted for word, or "" to
// leave word as is. ctx is the context of the previous token and next the
// first non-space byte after word.
func identQuote(word string, ctx int, next byte) string {
	var upper, lower bool
	for k := range len(word) {
		ch := word[k]
		upper = upper || ('A' <= ch && ch <= 'Z')
		lower = lower || ('a' <= ch && ch <= 'z')
	}

	if len(word) == 4 && strings.EqualFold(word, "user") {
		if ctx == identCtxDot || ctx == identCtxType || ctx == identCtxRoleKeyword {
			return ""
		}
		if upper && lower {
			return word
		}
		return "user"
	}

	if !upper || !lower {
		return ""
	}
	switch ctx {
	case identCtxType, identCtxCollate, identCtxExtractField:
		return ""
	}
	if next == '\'' || (next == '(' && ctx != identCtxRelation) {
		return ""
	}
	if isPgsqlKeyword(word) {
		return ""
	}
	return word
}

// identWordCtx returns the context that word gives to the next word.
// inCast reports that word is directly inside CAST( ... ).
func identWordCtx(word string, prev int, inCast bool) int {
	switch len(word) {
	case 2:
		if strings.EqualFold(word, "as") {
			if inCast {
				return identCtxType
			}
			return identCtxRelation // AS alias(col, ...)
		}
		if strings.EqualFold(word, "if") && prev == identCtxRelation {
			return identCtxRelation
		}
		if strings.EqualFold(word, "on") {
			return identCtxRelation
		}
	case 3:
		if strings.EqualFold(word, "not") && prev == identCtxRelation {
			return identCtxRelation
		}
		if strings.EqualFold(word, "for") {
			return identCtxRoleKeyword
		}
	case 4:
		if strings.EqualFold(word, "into") || strings.EqualFold(word, "view") || strings.EqualFold(word, "with") ||
			(prev == identCtxRelation && strings.EqualFold(word, "only")) {
			return identCtxRelation
		}
		if strings.EqualFold(word, "cast") {
			return identCtxCast
		}
		if strings.EqualFold(word, "drop") {
			return identCtxRoleKeyword
		}
	case 5:
		if strings.EqualFold(word, "table") {
			return identCtxRelation
		}
		if strings.EqualFold(word, "alter") {
			return identCtxRoleKeyword
		}
	case 6:
		if strings.EqualFold(word, "exists") && prev == identCtxRelation {
			return identCtxRelation
		}
		if strings.EqualFold(word, "create") {
			return identCtxRoleKeyword
		}
	case 7:
		if strings.EqualFold(word, "collate") {
			return identCtxCollate
		}
		if strings.EqualFold(word, "extract") {
			return identCtxExtract
		}
	case 9:
		if strings.EqualFold(word, "recursive") {
			return identCtxRelation
		}
	case 10:
		if strings.EqualFold(word, "references") {
			return identCtxRelation
		}
	}
	return identCtxNone
}

// isPgsqlKeyword reports whether word is a PostgreSQL keyword in any case.
func isPgsqlKeyword(word string) bool {
	if len(word) > pgsqlKeywordMaxLen {
		return false
	}
	var buf [pgsqlKeywordMaxLen]byte
	for k := range len(word) {
		ch := word[k]
		if 'A' <= ch && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		buf[k] = ch
	}
	_, ok := pgsqlKeywords[string(buf[:len(word)])]
	return ok
}

// skipQuoted returns the index after the '...' or "..." token at i
// (a doubled quote is an escaped quote; backslash escapes too if set).
func skipQuoted(sql string, i int, backslash bool) int {
	q := sql[i]
	n := len(sql)
	for j := i + 1; j < n; j++ {
		switch sql[j] {
		case '\\':
			if backslash {
				j++
			}
		case q:
			if j+1 < n && sql[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return n
}

// skipBlockComment returns the index after the (nested) /* */ comment at i.
func skipBlockComment(sql string, i int) int {
	n := len(sql)
	nesting := 0
	for i < n {
		switch {
		case i+1 < n && sql[i] == '/' && sql[i+1] == '*':
			nesting++
			i += 2
		case i+1 < n && sql[i] == '*' && sql[i+1] == '/':
			nesting--
			i += 2
			if nesting == 0 {
				return i
			}
		default:
			i++
		}
	}
	return n
}

// skipDollar returns the index after the $N param or $tag$...$tag$ literal
// at i (or after the "$" when it is neither).
func skipDollar(sql string, i int) int {
	n := len(sql)
	j := i + 1
	if j < n && isDigit(sql[j]) {
		for j < n && isDigit(sql[j]) {
			j++
		}
		return j
	}
	for j < n && isIdentStart(sql[j]) || (j > i+1 && j < n && isDigit(sql[j])) {
		j++
	}
	if j >= n || sql[j] != '$' {
		return i + 1
	}
	tag := sql[i : j+1]
	end := strings.Index(sql[j+1:], tag)
	if end < 0 {
		return n
	}
	return j + 1 + end + len(tag)
}

// skipNumber returns the index after the numeric literal at i
// (1, 1.5, .5, 1e-5, 0x1F, 1_000).
func skipNumber(sql string, i int) int {
	n := len(sql)
	j := i + 1
	for j < n {
		ch := sql[j]
		if isIdentChar(ch) || ch == '.' || ((ch == '+' || ch == '-') && (sql[j-1] == 'e' || sql[j-1] == 'E')) {
			j++
			continue
		}
		break
	}
	return j
}

// nextNonSpace returns the first non-space byte of sql at or after i (0 at the end).
func nextNonSpace(sql string, i int) byte {
	for ; i < len(sql); i++ {
		switch sql[i] {
		case ' ', '\t', '\n', '\r', '\f':
		default:
			return sql[i]
		}
	}
	return 0
}

func isDigit(c byte) bool {
	return '0' <= c && c <= '9'
}

func isIdentStart(c byte) bool {
	return c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || c >= 0x80
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || isDigit(c) || c == '$'
}
