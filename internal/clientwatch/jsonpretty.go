package clientwatch

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// A tool result that starts with JSON is shown INDENTED, with its keys in the order the seat wrote
// them. encoding/json cannot do that last part - it sorts map keys - and a reordered object reads as a
// different object, so the renderer here keeps the order and reproduces Python's json.dumps:
//
//   - insertion order, never sorted;
//   - its indent=2 separators, and its default ", " / ": " in the compact form;
//   - ensure_ascii=False for a DISPLAYED result (the module content publish-skills writes uses the
//     escaping form instead; that is a different job with a different requirement).
//
// This is a display path only: nothing here is ever compared byte-for-byte with anything.

type orderedJSONObject struct {
	keys   []string
	values []any
}

func (o *orderedJSONObject) get(key string) (any, bool) {
	for i, k := range o.keys {
		if k == key {
			return o.values[i], true
		}
	}
	return nil, false
}

// decodeOrderedJSON parses one JSON value, keeping object order.
func decodeOrderedJSON(text string) (any, int, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	value, err := decodeOrderedValue(decoder)
	if err != nil {
		return nil, 0, err
	}
	return value, int(decoder.InputOffset()), nil
}

func decodeOrderedValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := &orderedJSONObject{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, _ := keyToken.(string)
			value, err := decodeOrderedValue(decoder)
			if err != nil {
				return nil, err
			}
			object.keys = append(object.keys, key)
			object.values = append(object.values, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		items := []any{}
		for decoder.More() {
			value, err := decodeOrderedValue(decoder)
			if err != nil {
				return nil, err
			}
			items = append(items, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter %q", delim)
	}
}

// indentPython is json.dumps(value, indent=2, ensure_ascii=False).
func indentPython(value any) string {
	var builder strings.Builder
	writeDisplayJSON(&builder, value, 0)
	return builder.String()
}

// compactPython is json.dumps(value) with Python's ", " and ": " separators.
func compactPython(value any) string {
	var builder strings.Builder
	writeDisplayJSONCompact(&builder, value)
	return builder.String()
}

func writeDisplayJSON(builder *strings.Builder, value any, level int) {
	indent := strings.Repeat("  ", level+1)
	closer := strings.Repeat("  ", level)
	switch v := value.(type) {
	case *orderedJSONObject:
		if len(v.keys) == 0 {
			builder.WriteString("{}")
			return
		}
		builder.WriteString("{\n")
		for i, key := range v.keys {
			if i > 0 {
				builder.WriteString(",\n")
			}
			builder.WriteString(indent)
			writeDisplayString(builder, key)
			builder.WriteString(": ")
			writeDisplayJSON(builder, v.values[i], level+1)
		}
		builder.WriteString("\n" + closer + "}")
	case []any:
		if len(v) == 0 {
			builder.WriteString("[]")
			return
		}
		builder.WriteString("[\n")
		for i, item := range v {
			if i > 0 {
				builder.WriteString(",\n")
			}
			builder.WriteString(indent)
			writeDisplayJSON(builder, item, level+1)
		}
		builder.WriteString("\n" + closer + "]")
	case string:
		writeDisplayString(builder, v)
	case json.Number:
		builder.WriteString(string(v))
	case bool:
		if v {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case nil:
		builder.WriteString("null")
	default:
		builder.WriteString("null")
	}
}

func writeDisplayJSONCompact(builder *strings.Builder, value any) {
	switch v := value.(type) {
	case *orderedJSONObject:
		builder.WriteString("{")
		for i, key := range v.keys {
			if i > 0 {
				builder.WriteString(", ")
			}
			writeDisplayString(builder, key)
			builder.WriteString(": ")
			writeDisplayJSONCompact(builder, v.values[i])
		}
		builder.WriteString("}")
	case []any:
		builder.WriteString("[")
		for i, item := range v {
			if i > 0 {
				builder.WriteString(", ")
			}
			writeDisplayJSONCompact(builder, item)
		}
		builder.WriteString("]")
	case string:
		writeDisplayString(builder, v)
	case json.Number:
		builder.WriteString(string(v))
	case bool:
		if v {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	default:
		builder.WriteString("null")
	}
}

// writeDisplayString escapes exactly what Python's encoder escapes when ensure_ascii is False: the
// mandatory two, the five short control escapes, and \u00XX for the rest of C0.
func writeDisplayString(builder *strings.Builder, text string) {
	builder.WriteByte('"')
	for _, r := range text {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		default:
			if r < 0x20 {
				builder.WriteString(`\u`)
				hex := strconv.FormatInt(int64(r), 16)
				for len(hex) < 4 {
					hex = "0" + hex
				}
				builder.WriteString(hex)
				continue
			}
			// Python's ensure_ascii=False writes the rune through.
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
}
