package clientteam

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// orderedObject is a JSON object whose key order is preserved. An .mcp.json's `headers` and `env`
// carry that order into the module content the Python published, and content is compared
// byte-for-byte, so losing the order would turn a re-run's SKIP into a spurious NEW-VERSION.
type orderedObject struct {
	keys   []string
	values []any
}

func (o *orderedObject) get(key string) (any, bool) {
	for i, k := range o.keys {
		if k == key {
			return o.values[i], true
		}
	}
	return nil, false
}

// decodeOrdered parses raw JSON into strings, *orderedObject, []any, json.Number, bool and nil.
func decodeOrdered(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decodeOrderedValue(decoder)
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
		object := &orderedObject{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("object key is not a string")
			}
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

// orderedStrings converts an ordered object into the emitter's object, refusing anything whose values
// are not strings - the same rule the Python applies to an .mcp.json's headers and env.
func orderedStrings(object *orderedObject) (*pyObject, bool) {
	converted := newPyObject()
	for i, key := range object.keys {
		value, ok := object.values[i].(string)
		if !ok {
			return nil, false
		}
		converted.set(key, value)
	}
	return converted, true
}
