package clientteam

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// This file exists for ONE reason: a module's content is compared BYTE-FOR-BYTE with the body the
// cloud stores, and that comparison is what decides SKIP versus NEW-VERSION on a re-run. The Python
// built those bytes with `json.dumps`, and `encoding/json` cannot reproduce them:
//
//   - Go sorts map keys; Python keeps insertion order, and both an .mcp.json's `headers`/`env` and a
//     skill block's keys come from a file, so the order is data, not incidental;
//   - Go writes `{"a":1}` and `{"a":1,"b":2}` with no space after ':' or ','; Python's default
//     separators put one space after each (its compact form), and its indent=2 form puts each item on
//     its own line;
//   - Go escapes `<`, `>` and `&` as \u003c/… by default; Python does not;
//   - Python escapes every non-ASCII rune (ensure_ascii), Go writes it through.
//
// Any of those differences would make a re-run push a new immutable version where the Python skipped,
// which is a silent behaviour change on a verb whose whole job is idempotence. So the emitter is
// written out here, and moduleContent is the only way content is produced.

// pyObject is an insertion-ordered JSON object with Python's separators and escaping.
type pyObject struct {
	keys   []string
	values []any
}

func newPyObject() *pyObject { return &pyObject{} }

func (o *pyObject) set(key string, value any) *pyObject {
	o.keys = append(o.keys, key)
	o.values = append(o.values, value)
	return o
}

func (o *pyObject) has(key string) bool {
	_, found := o.get(key)
	return found
}

func (o *pyObject) get(key string) (any, bool) {
	for i, k := range o.keys {
		if k == key {
			return o.values[i], true
		}
	}
	return nil, false
}

// pyCompact renders json.dumps(value): items separated by ", ", keys from "key": value.
func pyCompact(value any) string {
	var b strings.Builder
	writePyJSON(&b, value, 0, false)
	return b.String()
}

// pyIndented renders json.dumps(value, indent=2): each item on its own line, two spaces per level.
func pyIndented(value any) string {
	var b strings.Builder
	writePyJSON(&b, value, 0, true)
	return b.String()
}

// writePyJSON writes value with Python's separators. indentLevel is the nesting depth of value
// itself; pretty selects json.dumps' indent=2 form over its default separators.
func writePyJSON(b *strings.Builder, value any, indentLevel int, pretty bool) {
	open := func(rune byte) {
		b.WriteByte(rune)
		if pretty {
			b.WriteByte('\n')
		}
	}
	closeWith := func(rune byte) {
		if pretty {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat("  ", indentLevel))
		}
		b.WriteByte(rune)
	}
	separator := func(index int) {
		if index == 0 {
			return
		}
		if pretty {
			b.WriteString(",\n")
			return
		}
		b.WriteString(", ")
	}
	itemIndent := func() {
		if pretty {
			b.WriteString(strings.Repeat("  ", indentLevel+1))
		}
	}
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writePyString(b, v)
	case []string:
		if len(v) == 0 {
			b.WriteString("[]")
			return
		}
		open('[')
		for i, item := range v {
			separator(i)
			itemIndent()
			writePyString(b, item)
		}
		closeWith(']')
	case *pyObject:
		if len(v.keys) == 0 {
			b.WriteString("{}")
			return
		}
		open('{')
		for i, key := range v.keys {
			separator(i)
			itemIndent()
			writePyString(b, key)
			b.WriteString(": ")
			writePyJSON(b, v.values[i], indentLevel+1, pretty)
		}
		closeWith('}')
	default:
		panic(fmt.Sprintf("clientteam: pyJSON cannot render %T", value))
	}
}

// writePyString reproduces Python's json string escaping: the two mandatory escapes, the five short
// control escapes, \u00XX for the rest of C0, and \uXXXX (with surrogate pairs) for everything else
// outside ASCII, because Python's encoder defaults to ensure_ascii=True.
func writePyString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				b.WriteString(`\u`)
				b.WriteString(pad4(strconv.FormatInt(int64(r), 16)))
			case r < 0x7f:
				if r == utf8.RuneError {
					// A lone surrogate cannot appear in a Go string; a replacement character is a
					// real rune and goes through as itself, which is what Python does too.
					b.WriteRune(r)
					continue
				}
				b.WriteRune(r)
			default:
				if r > 0xffff {
					high, low := utf16.EncodeRune(r)
					b.WriteString(`\u`)
					b.WriteString(pad4(strconv.FormatInt(int64(high), 16)))
					b.WriteString(`\u`)
					b.WriteString(pad4(strconv.FormatInt(int64(low), 16)))
					continue
				}
				b.WriteString(`\u`)
				b.WriteString(pad4(strconv.FormatInt(int64(r), 16)))
			}
		}
	}
	b.WriteByte('"')
}

func pad4(hex string) string {
	for len(hex) < 4 {
		hex = "0" + hex
	}
	return hex
}
