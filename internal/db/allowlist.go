package db

import (
	"fmt"
	"slices"
	"strings"
)

// tokenKind is what a token of SQL is.
type tokenKind int

const (
	space   tokenKind = iota - 1 // whitespace or a comment, which is no token
	word                         // a keyword or an identifier, unquoted
	quoted                       // an identifier in double quotes
	literal                      // a string, a dollar-quoted string or a number
	symbol                       // an operator or a punctuation mark
	escaped                      // a string or an identifier with Unicode escapes, U&'...' or U&"..."
)

// token is one token of a migration.
type token struct {
	kind       tokenKind
	text       string // as the migration writes it
	start, end int    // its offsets in the migration
}

// is reports whether the token is one of the keywords, in any case.
func (t token) is(kws ...string) bool {
	return t.kind == word && slices.ContainsFunc(kws, func(kw string) bool { return strings.EqualFold(t.text, kw) })
}

// isSymbol reports whether the token is one of the symbols.
func (t token) isSymbol(ss ...string) bool { return t.kind == symbol && slices.Contains(ss, t.text) }

// statement is one statement of a migration.
type statement struct {
	tokens []token
	text   string // as the migration writes it, from its first token to its last
	line   int    // the line it begins on
}

// firstLine returns the statement's first line, by which errors name it.
func (s *statement) firstLine() string {
	first, _, _ := strings.Cut(s.text, "\n")
	return strings.TrimSpace(first)
}

// split splits a migration into its statements: at each semicolon outside
// quotes, dollar-quoted bodies and comments.
func split(sql string) ([]statement, error) {
	toks, err := lex(sql)
	if err != nil {
		return nil, err
	}
	var stmts []statement
	begin := 0
	for i := 0; i <= len(toks); i++ {
		if i < len(toks) && !toks[i].isSymbol(";") {
			continue
		}
		if i > begin {
			first, last := toks[begin], toks[i-1]
			stmts = append(stmts, statement{tokens: toks[begin:i], text: sql[first.start:last.end], line: line(sql, first.start)})
		}
		begin = i + 1
	}
	return stmts, nil
}

// line returns the line of sql that offset i is on.
func line(sql string, i int) int { return 1 + strings.Count(sql[:i], "\n") }

// lex reads SQL as tokens, without its whitespace and comments.
func lex(sql string) ([]token, error) {
	var toks []token
	for i := 0; i < len(sql); {
		kind, end, err := next(sql, i)
		if err != nil {
			return nil, err
		}
		if kind != space {
			toks = append(toks, token{kind: kind, text: sql[i:end], start: i, end: end})
		}
		i = end
	}
	return toks, nil
}

// next reads what begins at sql[i]: it returns its kind and where it ends.
func next(sql string, i int) (tokenKind, int, error) {
	rest := sql[i:]
	switch c := sql[i]; {
	case strings.IndexByte(" \t\n\r\f\v", c) >= 0:
		return space, i + 1, nil
	case strings.HasPrefix(rest, "--"):
		if end := strings.IndexByte(rest, '\n'); end >= 0 {
			return space, i + end + 1, nil
		}
		return space, len(sql), nil
	case strings.HasPrefix(rest, "/*"):
		end, err := commentEnd(sql, i)
		return space, end, err
	case c == '\'':
		end, err := quoteEnd(sql, i, false)
		return literal, end, err
	case strings.HasPrefix(rest, "E'"), strings.HasPrefix(rest, "e'"):
		end, err := quoteEnd(sql, i+1, true)
		return literal, end, err
	case c == '"':
		end, err := quoteEnd(sql, i, false)
		return quoted, end, err
	case dollarTag(rest) != "":
		end, err := dollarEnd(sql, i)
		return literal, end, err
	case opensUnicodeEscapes(rest):
		end, err := quoteEnd(sql, i+2, false)
		return escaped, end, err
	case isIdentStart(c):
		return word, identEnd(sql, i), nil
	case isDigit(c):
		return literal, numberEnd(sql, i), nil
	case strings.HasPrefix(rest, "::"):
		return symbol, i + 2, nil
	}
	return symbol, i + 1, nil
}

// opensUnicodeEscapes reports whether s begins a string or an identifier
// with Unicode escapes: U& and a quote, with nothing between, which Postgres
// reads as the text the escapes spell.
func opensUnicodeEscapes(s string) bool {
	return len(s) > 2 && (s[0] == 'U' || s[0] == 'u') && s[1] == '&' && (s[2] == '\'' || s[2] == '"')
}

// identEnd returns where the keyword or identifier that begins at sql[i]
// ends.
func identEnd(sql string, i int) int {
	for i++; i < len(sql) && (isIdentStart(sql[i]) || isDigit(sql[i]) || sql[i] == '$'); i++ {
	}
	return i
}

func isIdentStart(c byte) bool {
	return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c >= 0x80
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// quoteEnd returns where the quoted string or identifier that opens at
// sql[i] ends: at the quote that closes it, which a doubled quote does not,
// nor, in an escape string, one after a backslash.
func quoteEnd(sql string, i int, escapes bool) (int, error) {
	q := sql[i]
	for j := i + 1; j < len(sql); j++ {
		switch {
		case escapes && sql[j] == '\\':
			j++
		case sql[j] != q:
		case j+1 < len(sql) && sql[j+1] == q:
			j++
		default:
			return j + 1, nil
		}
	}
	return 0, fmt.Errorf("line %d: the quote %c opens is never closed", line(sql, i), q)
}

// commentEnd returns where the block comment that opens at sql[i] ends.
// Block comments nest.
func commentEnd(sql string, i int) (int, error) {
	depth := 0
	for j := i; j+1 < len(sql); j++ {
		switch sql[j : j+2] {
		case "/*":
			depth++
			j++
		case "*/":
			depth--
			j++
			if depth == 0 {
				return j + 1, nil
			}
		}
	}
	return 0, fmt.Errorf("line %d: the comment /* opens is never closed", line(sql, i))
}

// dollarTag returns the tag of the dollar-quoted string that s begins with,
// such as $$ or $body$, or "" when it begins with none, as $1 does.
func dollarTag(s string) string {
	if !strings.HasPrefix(s, "$") {
		return ""
	}
	for j := 1; j < len(s); j++ {
		switch c := s[j]; {
		case c == '$':
			return s[:j+1]
		case isIdentStart(c), j > 1 && isDigit(c):
		default:
			return ""
		}
	}
	return ""
}

// dollarEnd returns where the dollar-quoted string that opens at sql[i]
// ends: after its tag again.
func dollarEnd(sql string, i int) (int, error) {
	tag := dollarTag(sql[i:])
	end := strings.Index(sql[i+len(tag):], tag)
	if end < 0 {
		return 0, fmt.Errorf("line %d: the string %s opens is never closed", line(sql, i), tag)
	}
	return i + len(tag) + end + len(tag), nil
}

// numberEnd returns where the number that begins at sql[i] ends: digits,
// points, underscores, letters, as of an exponent or a base, and an
// exponent's sign.
func numberEnd(sql string, i int) int {
	for i++; i < len(sql); i++ {
		c := sql[i]
		sign := (c == '+' || c == '-') && (sql[i-1] == 'e' || sql[i-1] == 'E')
		if !sign && !isDigit(c) && c != '.' && (!isIdentStart(c) || c >= 0x80) {
			return i
		}
	}
	return i
}

// kind is what a statement does, as far as the allowlist tells statements
// apart.
type kind string

const (
	createTable kind = "CREATE TABLE"
	createIndex kind = "CREATE INDEX"
	uniqueIndex kind = "CREATE UNIQUE INDEX"
	addColumns  kind = "ALTER TABLE ... ADD COLUMN"
	alterTable  kind = "ALTER TABLE"
	function    kind = "CREATE FUNCTION"
	trigger     kind = "CREATE TRIGGER"
	doBlock     kind = "DO"
	lockTable   kind = "LOCK"
	analyze     kind = "ANALYZE"
	dataChange  kind = "INSERT, UPDATE or DELETE"
)

// kinds tells a statement's kind by its first keywords.
var kinds = []struct {
	keywords []string
	kind     kind
}{
	{[]string{"CREATE", "TABLE"}, createTable},
	{[]string{"CREATE", "INDEX"}, createIndex},
	{[]string{"CREATE", "UNIQUE", "INDEX"}, uniqueIndex},
	{[]string{"CREATE", "FUNCTION"}, function},
	{[]string{"CREATE", "OR", "REPLACE", "FUNCTION"}, function},
	{[]string{"CREATE", "TRIGGER"}, trigger},
	{[]string{"CREATE", "OR", "REPLACE", "TRIGGER"}, trigger},
	{[]string{"CREATE", "CONSTRAINT", "TRIGGER"}, trigger},
	{[]string{"ALTER", "TABLE"}, alterTable},
	{[]string{"DO"}, doBlock},
	{[]string{"LOCK"}, lockTable},
	{[]string{"ANALYZE"}, analyze},
	{[]string{"INSERT"}, dataChange},
	{[]string{"UPDATE"}, dataChange},
	{[]string{"DELETE"}, dataChange},
}

// classify returns a statement's kind; an ALTER TABLE that does nothing but
// add columns is of a kind of its own. A statement of no kind the allowlist
// knows does not classify.
func classify(s *statement) (kind, error) {
	for _, k := range kinds {
		if skip(s.tokens, 0, k.keywords...) == 0 {
			continue
		}
		if k.kind == alterTable {
			if _, _, ok := addedColumns(s); ok {
				return addColumns, nil
			}
		}
		return k.kind, nil
	}
	return "", fmt.Errorf("line %d: the allowlist knows no statement of its kind: %s", s.line, s.firstLine())
}

// offList says why the statements of some kinds are off the allowlist. Those
// of any other kind but the three on it are off it for adding no column,
// table or index.
var offList = map[kind]string{
	uniqueIndex: "a unique index refuses rows an older build may write",
	alterTable:  "it changes a table older builds use",
	function:    "it creates or replaces a function, as a chain form is",
	trigger:     "it creates a trigger",
	doBlock:     "a DO block runs SQL the allowlist cannot read",
	dataChange:  "it writes rows, as a backfill does",
}

// allowlisted returns nil when every statement of the migration is on the
// allowlist, and otherwise an error that names the first that is not.
// audited holds the tables that a trigger names.
//
// A migration may declare that builds older than itself read the schema it
// leaves (Contract C8) only when every statement of it is one they can read
// (Plan 7d-1, Decision 18):
//
//   - a column added, nullable or with a constant default, to a table that no
//     trigger names;
//   - a table added that no trigger names;
//   - an index added that is not unique.
//
// The tables a trigger names are the audited ones: a chain form lists their
// columns, so a column added to one would go unchained. Constraints,
// triggers, functions (chain forms among them), changed defaults, backfills,
// rewrites, dynamic SQL and any other statement are off the allowlist. A
// test holds every embedded migration that declares an older reader to it;
// nothing checks it as a migration runs.
func allowlisted(sql string, audited map[string]bool) error {
	stmts, err := split(sql)
	if err != nil {
		return err
	}
	for i := range stmts {
		s := &stmts[i]
		if why := whyOff(s, audited); why != "" {
			return fmt.Errorf("statement %d, on line %d, is off the allowlist (%s): %s", i+1, s.line, why, s.firstLine())
		}
	}
	return nil
}

// whyOff returns why a statement is off the allowlist, or "" when it is on
// it.
func whyOff(s *statement, audited map[string]bool) string {
	if slices.ContainsFunc(s.tokens, func(t token) bool { return t.kind == escaped }) {
		return "it spells a name or a string with Unicode escapes (U&), which the allowlist does not read"
	}
	k, err := classify(s)
	if err != nil {
		return "the allowlist knows no statement of its kind"
	}
	switch k {
	case createIndex:
		return ""
	case createTable:
		table, after, ok := name(s.tokens, skip(s.tokens, 2, "IF", "NOT", "EXISTS"))
		switch {
		case !ok:
			return "it names no table"
		case audited[table]:
			return "a trigger names table " + table
		}
		return madeFromAnother(s.tokens[after:])
	case addColumns:
		table, cols, _ := addedColumns(s)
		if audited[table] {
			return "a trigger names table " + table + ", whose chain form lists its columns"
		}
		for _, c := range cols {
			if why := c.whyOff(); why != "" {
				return why
			}
		}
		return ""
	}
	if why, ok := offList[k]; ok {
		return why
	}
	return "it adds no column, table or index"
}

// madeFromAnother returns why a table that CREATE TABLE makes from another
// table, or from a query, is off the allowlist, or "" when it makes it from
// neither: toks follow the table's name. Outside the parentheses, INHERITS
// makes it a child of other tables, PARTITION OF a partition of one, and AS
// fills it from a query. A table made LIKE another copies only its
// definition, and is a table added as any other.
func madeFromAnother(toks []token) string {
	depth := 0
	for i, t := range toks {
		switch {
		case t.isSymbol("("):
			depth++
		case t.isSymbol(")"):
			depth--
		case depth > 0:
		case t.is("INHERITS"):
			return "its rows show in reads of the tables it INHERITS, whose triggers do not act on them"
		case t.is("PARTITION") && i+1 < len(toks) && toks[i+1].is("OF"):
			return "it is a PARTITION OF another table, whose reads show its rows"
		case t.is("AS"):
			return "it is made AS a query, which copies rows and may call functions that write"
		}
	}
	return ""
}

// skip returns the index after the keywords at toks[i:], or i when they are
// not there.
func skip(toks []token, i int, kws ...string) int {
	if len(toks) < i+len(kws) {
		return i
	}
	for j, kw := range kws {
		if !toks[i+j].is(kw) {
			return i
		}
	}
	return i + len(kws)
}

// name returns what a name at toks[i:], qualified or not, names: its last
// part, in lower case unless quoted, and the index after it.
func name(toks []token, i int) (part string, after int, ok bool) {
	for ; i < len(toks); i += 2 {
		switch t := toks[i]; t.kind {
		case word:
			part = strings.ToLower(t.text)
		case quoted:
			part = strings.ReplaceAll(t.text[1:len(t.text)-1], `""`, `"`)
		default:
			return "", i, false
		}
		if i+1 == len(toks) || !toks[i+1].isSymbol(".") {
			return part, i + 1, true
		}
	}
	return "", i, false
}

// column is a column an ALTER TABLE adds: its name, then its type and its
// clauses.
type column struct {
	name string
	def  []token
}

// addedColumns returns the table an ALTER TABLE alters and the columns it
// adds, and whether adding columns is all it does.
func addedColumns(s *statement) (string, []column, bool) {
	toks := s.tokens
	table, i, ok := name(toks, skip(toks, skip(toks, 2, "IF", "EXISTS"), "ONLY"))
	if !ok {
		return "", nil, false
	}
	if i < len(toks) && toks[i].isSymbol("*") {
		i++
	}
	var cols []column
	for _, action := range splitAtCommas(toks[i:]) {
		j := skip(action, 0, "ADD")
		if j == 0 || len(action) < 2 || action[1].is("CONSTRAINT", "CHECK", "UNIQUE", "PRIMARY", "FOREIGN", "EXCLUDE") {
			return table, nil, false
		}
		col, j, ok := name(action, skip(action, skip(action, j, "COLUMN"), "IF", "NOT", "EXISTS"))
		if !ok {
			return table, nil, false
		}
		cols = append(cols, column{name: col, def: action[j:]})
	}
	return table, cols, true
}

// splitAtCommas splits tokens at the commas outside parentheses.
func splitAtCommas(toks []token) [][]token {
	var parts [][]token
	depth, begin := 0, 0
	for i, t := range toks {
		switch {
		case t.isSymbol("("):
			depth++
		case t.isSymbol(")"):
			depth--
		case t.isSymbol(",") && depth == 0:
			parts = append(parts, toks[begin:i])
			begin = i + 1
		}
	}
	return append(parts, toks[begin:])
}

// clauses are the keywords that may follow a column's type.
var clauses = []string{"NOT", "NULL", "DEFAULT", "CONSTRAINT", "CHECK", "UNIQUE", "PRIMARY", "REFERENCES", "GENERATED", "COLLATE"}

// serials are the types that fill a new column of every row from a sequence.
var serials = []string{"serial", "bigserial", "smallserial", "serial2", "serial4", "serial8"}

// whyOff returns why adding the column is off the allowlist, or "" when it
// is on it: it is nullable or has a default other than NULL, the default is a
// constant, and it comes with no other clause.
func (c column) whyOff() string {
	// Postgres reads a serial's name as it reads any name: unquoted names in
	// lower case, quoted ones as written, and a type modifier changes nothing.
	if typ, _, ok := name(c.def, 0); ok && slices.Contains(serials, typ) {
		return "column " + c.name + " is a serial, which fills every row from a sequence"
	}
	notNull, defaulted := false, false
	for i := typeEnd(c.def); i < len(c.def); {
		switch t := c.def[i]; {
		case t.is("NOT") && i+1 < len(c.def) && c.def[i+1].is("NULL"):
			notNull = true
			i += 2
		case t.is("NULL"):
			i++
		case t.is("DEFAULT"):
			// The expression's first token is its own, NULL included.
			end := min(i+2, len(c.def))
			for end < len(c.def) && !c.def[end].is(clauses...) {
				end++
			}
			expr := c.def[i+1 : end]
			if !isConstant(expr) {
				return "the default of column " + c.name + " is not a constant"
			}
			// A NULL default, cast or not, gives older builds' inserts nothing.
			defaulted = !unsigned(expr)[0].is("NULL")
			i = end
		default:
			return "column " + c.name + " comes with " + strings.ToUpper(t.text) + ", which older builds' writes may not meet"
		}
	}
	if notNull && !defaulted {
		return "column " + c.name + " is NOT NULL without a default other than NULL, and older builds' inserts leave it out"
	}
	return ""
}

// typeEnd returns where a column's type ends: at its first clause outside
// parentheses.
func typeEnd(def []token) int {
	depth := 0
	for i, t := range def {
		switch {
		case t.isSymbol("("):
			depth++
		case t.isSymbol(")"):
			depth--
		case depth == 0 && t.is(clauses...):
			return i
		}
	}
	return len(def)
}

// unsigned returns an expression without the sign it begins with, if any.
func unsigned(expr []token) []token {
	if len(expr) > 0 && expr[0].isSymbol("-", "+") {
		return expr[1:]
	}
	return expr
}

// isConstant reports whether an expression is a constant: a literal, TRUE,
// FALSE or NULL, signed or cast, as -1 or '{}'::jsonb are.
func isConstant(expr []token) bool {
	expr = unsigned(expr)
	if len(expr) == 0 || expr[0].kind != literal && !expr[0].is("TRUE", "FALSE", "NULL") {
		return false
	}
	// What follows may only cast it: ::, a type and the type's modifiers.
	if len(expr) > 1 && !expr[1].isSymbol("::") {
		return false
	}
	for _, t := range expr[1:] {
		if t.kind == symbol && !t.isSymbol("::", ".", "(", ")", ",", "[", "]") {
			return false
		}
	}
	return true
}

// auditedTables returns the tables that a trigger names in any of the
// migrations: each CREATE TRIGGER ... ON <table>. It fails on a trigger whose
// table it cannot read, as when Unicode escapes spell its name: the table
// could be an audited one, which every migration's check must then know.
func auditedTables(sqls ...string) (map[string]bool, error) {
	tables := map[string]bool{}
	for _, sql := range sqls {
		stmts, err := split(sql)
		if err != nil {
			return nil, err
		}
		for i := range stmts {
			s := &stmts[i]
			if k, err := classify(s); err != nil || k != trigger {
				continue
			}
			on := slices.IndexFunc(s.tokens, func(t token) bool { return t.is("ON") })
			table, _, ok := name(s.tokens, on+1)
			if on < 0 || !ok {
				return nil, fmt.Errorf("statement %d, on line %d, is a trigger on a table the allowlist cannot read: %s", i+1, s.line, s.firstLine())
			}
			tables[table] = true
		}
	}
	return tables, nil
}
