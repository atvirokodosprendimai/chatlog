// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// object is a JSON object that keeps its keys in file order and every value it does not touch as it was, so a merge
// into someone's settings changes only what it means to change.
type object []member

type member struct {
	key   string
	value json.RawMessage
}

// parseObject reads a JSON object; an empty or blank input is an empty object.
func parseObject(b []byte) (object, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return object{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	o := object{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o = append(o, member{tok.(string), v})
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("text after the object")
	}
	return o, nil
}

func (o object) get(k string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.key == k {
			return m.value, true
		}
	}
	return nil, false
}

// set replaces a key's value where it stands, or appends the key.
func (o *object) set(k string, v json.RawMessage) {
	for i, m := range *o {
		if m.key == k {
			(*o)[i].value = v
			return
		}
	}
	*o = append(*o, member{k, v})
}

func (o *object) del(k string) {
	out := (*o)[:0]
	for _, m := range *o {
		if m.key != k {
			out = append(out, m)
		}
	}
	*o = out
}

// MarshalJSON writes the members in order.
func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// subObject reads a member that must be an object, empty when absent.
func (o object) subObject(k string) (object, error) {
	v, ok := o.get(k)
	if !ok || string(v) == "null" {
		return object{}, nil
	}
	sub, err := parseObject(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", k, err)
	}
	return sub, nil
}

// raw marshals a value built here, without HTML escaping, as a person writes JSON.
func raw(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err) // only values built here are marshalled
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

// pretty is the file's text: two-space indent, as Claude Code writes its own. json.Indent leaves the strings as they
// were; json.MarshalIndent would rewrite <, > and & in someone's values as \u escapes.
func pretty(o object) ([]byte, error) {
	b, _ := o.MarshalJSON()
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}
