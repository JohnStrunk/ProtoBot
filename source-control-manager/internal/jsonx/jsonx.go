// Package jsonx encodes the SCM's JSON documents deterministically: object
// keys keep the order the code gives them, and HTML characters are not
// escaped, so the same result always has the same bytes.
package jsonx

import (
	"bytes"
	"encoding/json"
)

// Field is one key and value of an ordered object.
type Field struct {
	Key   string
	Value any
}

// Obj is a JSON object whose keys keep their insertion order. A nil Obj
// encodes as {}.
type Obj []Field

// F builds a Field.
func F(key string, value any) Field { return Field{Key: key, Value: value} }

// O builds an Obj from fields.
func O(fields ...Field) Obj {
	if fields == nil {
		return Obj{}
	}
	return Obj(fields)
}

// Get returns the value of key and whether it is present.
func (o Obj) Get(key string) (any, bool) {
	for _, f := range o {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

// MarshalJSON writes the fields in order.
func (o Obj) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := Marshal(f.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		value, err := Marshal(f.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Marshal encodes v compactly, without HTML escaping and without a
// trailing newline.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
