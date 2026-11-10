package pages

// part is one piece of a line (design D11; R171): the platform's words, or a
// stored value with how it shows. A line is []part, built in Go and rendered
// by the template "line"; no Go code joins a stored value into a string.
type part struct {
	Words string  // the platform's words; a time, a number or a code it formats counts as words
	Value string  // a stored value; empty for words
	As    showing // how Value shows
	Href  string  // a link's path or anchor, built from ids by url.PathEscape alone
}

// showing is how a part's value shows (the words of the board), each kind
// through its display function, inside its <bdi> (R154).
type showing int

// The kinds of a part.
const (
	asWords  showing = iota // Words, as text
	asName                  // a name, key, id, subject or routing key: <bdi>{{inertLine .Value}}</bdi>
	asValue                 // a single-line value as quoted data: <q class="data"><bdi>{{inertLine .Value}}</bdi></q>
	asQuote                 // a quote or a title as quoted data: <q class="data"><bdi>{{inertText .Value}}</bdi></q>
	asModel                 // model-written text: model-written: <span class="model"><bdi>{{inertText .Value}}</bdi></span>
	asPerson                // a person's words: a person's words: <bdi>{{inertText .Value}}</bdi>
)

// line is a sentence of the platform's words and stored values, as parts
// (R171), which the template "line" renders.
type line []part

// The template "part" asks a part's kind by these.
func (p part) IsName() bool   { return p.As == asName }
func (p part) IsValue() bool  { return p.As == asValue }
func (p part) IsQuote() bool  { return p.As == asQuote }
func (p part) IsModel() bool  { return p.As == asModel }
func (p part) IsPerson() bool { return p.As == asPerson }

// The parts a line is built of, one function a kind, so that a stored value
// never goes in as words: each returns l with the part added after its own.
func (l line) say(words string) line { return l.add(part{Words: words}) }
func (l line) name(v string) line    { return l.add(part{Value: v, As: asName}) }
func (l line) value(v string) line   { return l.add(part{Value: v, As: asValue}) }
func (l line) quote(v string) line   { return l.add(part{Value: v, As: asQuote}) }
func (l line) model(v string) line   { return l.add(part{Value: v, As: asModel}) }
func (l line) person(v string) line  { return l.add(part{Value: v, As: asPerson}) }

// code adds c, a code, in words (Task 5's review, ruling U): m's words for
// it, the platform's; or, for a code m holds no words for, the code itself,
// a name, inert in its <bdi>, so that no code a store holds is ever shown
// among the platform's words. Every lookup of a code in a words table goes
// through it.
func (l line) code(m map[string]string, c string) line {
	if words, ok := m[c]; ok {
		return l.say(words)
	}
	return l.name(c)
}

// link adds the platform's words as a link to href, and nameLink a name.
func (l line) link(words, href string) line { return l.add(part{Words: words, Href: href}) }
func (l line) nameLink(v, href string) line { return l.add(part{Value: v, As: asName, Href: href}) }

// add is l with p after its parts, in an array of its own, so that two lines
// built on one prefix never share a part (see TestLinesBuiltOnOnePrefixShareNothing).
func (l line) add(p part) line { return append(l[:len(l):len(l)], p) }
