package cli

import (
	"bytes"
	"encoding/json"
)

// llama.cpp turns a JSON schema into a grammar that generates an object's
// properties in the order the schema lists them. That order is part of the
// prompt: a "thought" listed before an "action" is written first, so the
// model reasons before it commits. Go sorts map keys when it marshals, so
// schemas whose property order matters are built from props instead.

// prop is one property of an object schema.
type prop struct {
	name   string
	schema any
}

// props marshals as a JSON object that keeps its order.
type props []prop

func (ps props) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range ps {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(p.name)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(p.schema)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// object is a closed object schema whose properties are all required and
// generated in the order given.
func object(ps ...prop) map[string]any {
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.name
	}
	return map[string]any{"type": "object", "properties": props(ps), "required": names, "additionalProperties": false}
}

// mustJSON marshals a schema built from literals, which cannot fail.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
