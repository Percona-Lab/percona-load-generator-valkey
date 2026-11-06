// Package rawcmd parses the text of a raw command (for example "SET {key}
// {value} EX 60"), expands its placeholders for each call, and refuses commands
// that could damage the server under test unless that is explicitly allowed.
package rawcmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Source provides the data placeholders expand to. valkey.Gen implements it.
type Source interface {
	Key(typ string) string  // a random existing key of a type ("string", "hash", ...)
	Value() string          // a random value of the dataset's value size
	Int(lo, hi int64) int64 // a random integer in [lo, hi]
	N() int64               // a random key index in [0, key_count)
	NowMillis() int64       // current Unix time in milliseconds
}

type partKind int

const (
	literal partKind = iota
	phKey
	phValue
	phInt
	phN
	phNow
)

type part struct {
	kind   partKind
	text   string // literal text, or the key type for phKey
	lo, hi int64
}

// Template is a parsed command. Each argument is a sequence of parts (literal
// text and placeholders), so "user:{n}" is a literal followed by a placeholder.
type Template struct {
	Text string
	args [][]part
	// KeyArg is the index (in the command, counting the name as 0) of the first
	// {key} placeholder argument, which a cluster uses to route the command, or -1.
	KeyArg int
}

// Name returns the upper-cased command name.
func (t *Template) Name() string {
	if len(t.args) == 0 {
		return ""
	}
	return strings.ToUpper(literalText(t.args[0]))
}

// Sub returns the upper-cased first argument when it is plain text (the
// subcommand of CONFIG, CLIENT, CLUSTER ...), or "".
func (t *Template) Sub() string {
	if len(t.args) < 2 || !allLiteral(t.args[1]) {
		return ""
	}
	return strings.ToUpper(literalText(t.args[1]))
}

// HasArg reports whether a plain-text argument equals s (case-insensitive).
func (t *Template) HasArg(s string) bool {
	for _, a := range t.args[1:] {
		if allLiteral(a) && strings.EqualFold(literalText(a), s) {
			return true
		}
	}
	return false
}

func allLiteral(ps []part) bool {
	for _, p := range ps {
		if p.kind != literal {
			return false
		}
	}
	return true
}

func literalText(ps []part) string {
	var b strings.Builder
	for _, p := range ps {
		if p.kind == literal {
			b.WriteString(p.text)
		}
	}
	return b.String()
}

// Parse reads one command from text. Arguments are separated by whitespace;
// single or double quotes group words, and a backslash escapes the next
// character. Placeholders: {key}, {key:TYPE}, {value}, {n}, {int:LO-HI}, {now}.
func Parse(text string) (*Template, error) {
	words, err := split(text)
	if err != nil {
		return nil, err
	}
	if len(words) == 0 {
		return nil, errors.New("the command is empty")
	}
	t := &Template{Text: strings.TrimSpace(text), KeyArg: -1}
	for i, w := range words {
		ps, err := parseWord(w)
		if err != nil {
			return nil, err
		}
		t.args = append(t.args, ps)
		if t.KeyArg < 0 && i > 0 {
			for _, p := range ps {
				if p.kind == phKey {
					t.KeyArg = i
				}
			}
		}
	}
	if !allLiteral(t.args[0]) {
		return nil, errors.New("the command name cannot be a placeholder")
	}
	if n := t.Name(); n == "" || !(n[0] >= 'A' && n[0] <= 'Z') {
		return nil, fmt.Errorf("%q is not a command name (it must start with a letter)", t.Name())
	}
	return t, nil
}

// split tokenizes with shell-like quoting. A word keeps the raw text of its
// quoted pieces without the quotes.
func split(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in, quote := false, rune(0)
	esc := false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && quote != '\'':
			esc, in = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, in = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", quote)
	}
	if esc {
		return nil, errors.New("a backslash at the end has nothing to escape")
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}

func parseWord(w string) ([]part, error) {
	var parts []part
	for len(w) > 0 {
		i := strings.IndexByte(w, '{')
		if i < 0 {
			parts = append(parts, part{kind: literal, text: w})
			break
		}
		j := strings.IndexByte(w[i:], '}')
		if j < 0 {
			// An unmatched brace is literal text (hash tags use braces too).
			parts = append(parts, part{kind: literal, text: w})
			break
		}
		name := w[i+1 : i+j]
		ph, ok, err := placeholder(name)
		if err != nil {
			return nil, err
		}
		if !ok { // something like a hash tag {user1}: keep it as text
			parts = append(parts, part{kind: literal, text: w[:i+j+1]})
			w = w[i+j+1:]
			continue
		}
		if i > 0 {
			parts = append(parts, part{kind: literal, text: w[:i]})
		}
		parts = append(parts, ph)
		w = w[i+j+1:]
	}
	return mergeLiterals(parts), nil
}

func mergeLiterals(ps []part) []part {
	var out []part
	for _, p := range ps {
		if p.kind == literal && len(out) > 0 && out[len(out)-1].kind == literal {
			out[len(out)-1].text += p.text
			continue
		}
		out = append(out, p)
	}
	return out
}

var keyTypes = map[string]bool{"string": true, "hash": true, "list": true, "set": true, "zset": true, "stream": true, "geo": true, "hll": true, "bitmap": true}

// placeholder recognizes a placeholder name; ok is false for ordinary braces.
func placeholder(name string) (part, bool, error) {
	switch {
	case name == "key":
		return part{kind: phKey, text: "string"}, true, nil
	case strings.HasPrefix(name, "key:"):
		typ := name[4:]
		if !keyTypes[typ] {
			return part{}, false, fmt.Errorf("{%s}: unknown key type %q (want string, hash, list, set, zset, stream, geo, hll or bitmap)", name, typ)
		}
		return part{kind: phKey, text: typ}, true, nil
	case name == "value":
		return part{kind: phValue}, true, nil
	case name == "n":
		return part{kind: phN}, true, nil
	case name == "now":
		return part{kind: phNow}, true, nil
	case strings.HasPrefix(name, "int:"):
		lo, hi, ok := strings.Cut(name[4:], "-")
		a, err1 := strconv.ParseInt(lo, 10, 64)
		b, err2 := strconv.ParseInt(hi, 10, 64)
		if !ok || err1 != nil || err2 != nil || a > b {
			return part{}, false, fmt.Errorf("{%s}: want {int:LOW-HIGH} with LOW <= HIGH", name)
		}
		return part{kind: phInt, lo: a, hi: b}, true, nil
	}
	return part{}, false, nil
}

// Expand builds the argument list for one call.
func (t *Template) Expand(src Source) []string {
	out := make([]string, len(t.args))
	for i, ps := range t.args {
		if len(ps) == 1 && ps[0].kind == literal {
			out[i] = ps[0].text
			continue
		}
		var b strings.Builder
		for _, p := range ps {
			switch p.kind {
			case literal:
				b.WriteString(p.text)
			case phKey:
				b.WriteString(src.Key(p.text))
			case phValue:
				b.WriteString(src.Value())
			case phN:
				b.WriteString(strconv.FormatInt(src.N(), 10))
			case phInt:
				b.WriteString(strconv.FormatInt(src.Int(p.lo, p.hi), 10))
			case phNow:
				b.WriteString(strconv.FormatInt(src.NowMillis(), 10))
			}
		}
		out[i] = b.String()
	}
	return out
}
