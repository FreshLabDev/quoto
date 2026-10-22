// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// unsupportedKeywords are what Claude's structured outputs reject: numeric
// bounds, string lengths and maxItems. The parsers clamp scores and clip
// strings themselves, so Claude gets a copy of each schema without them and
// OpenRouter keeps the full one.
var unsupportedKeywords = map[string]bool{
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"multipleOf": true, "minLength": true, "maxLength": true, "maxItems": true,
}

// StripUnsupported returns a copy of a JSON schema without the keywords Claude
// rejects, with the key order of the input kept: the model writes its answer in
// the order of the schema. The members of "properties" are field names, not
// keywords, so a field called "maximum" stays, though a "maximum" keyword inside
// its own schema goes.
func StripUnsupported(schema []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(schema))
	dec.UseNumber()
	root, err := readNode(dec)
	if err != nil {
		return nil, fmt.Errorf("claude: schema: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("claude: schema: trailing data")
	}
	var out bytes.Buffer
	if err := writeNode(&out, strip(root)); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// node is a JSON value that remembers the order of its object keys.
type node struct {
	kind   byte // 'o' object, 'a' array, 's' scalar
	keys   []string
	vals   []*node
	scalar any // string, json.Number, bool or nil
}

func readNode(dec *json.Decoder) (*node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return &node{kind: 's', scalar: tok}, nil
	}
	switch delim {
	case '{':
		n := &node{kind: 'o'}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			child, err := readNode(dec)
			if err != nil {
				return nil, err
			}
			n.keys = append(n.keys, keyTok.(string))
			n.vals = append(n.vals, child)
		}
		_, err := dec.Token() // }
		return n, err
	case '[':
		n := &node{kind: 'a'}
		for dec.More() {
			child, err := readNode(dec)
			if err != nil {
				return nil, err
			}
			n.vals = append(n.vals, child)
		}
		_, err := dec.Token() // ]
		return n, err
	}
	return nil, fmt.Errorf("unexpected delimiter %v", delim)
}

func strip(n *node) *node {
	switch n.kind {
	case 'a':
		out := &node{kind: 'a'}
		for _, v := range n.vals {
			out.vals = append(out.vals, strip(v))
		}
		return out
	case 'o':
		out := &node{kind: 'o'}
		for i, key := range n.keys {
			val := n.vals[i]
			switch {
			case key == "properties" && val.kind == 'o':
				props := &node{kind: 'o'}
				for j, name := range val.keys {
					props.keys = append(props.keys, name)
					props.vals = append(props.vals, strip(val.vals[j]))
				}
				out.keys, out.vals = append(out.keys, key), append(out.vals, props)
			case !unsupportedKeywords[key]:
				out.keys, out.vals = append(out.keys, key), append(out.vals, strip(val))
			}
		}
		return out
	}
	return n
}

func writeNode(buf *bytes.Buffer, n *node) error {
	switch n.kind {
	case 'o':
		buf.WriteByte('{')
		for i, key := range n.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeScalar(buf, key); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeNode(buf, n.vals[i]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case 'a':
		buf.WriteByte('[')
		for i, v := range n.vals {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeNode(buf, v); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	default:
		return writeScalar(buf, n.scalar)
	}
	return nil
}

func writeScalar(buf *bytes.Buffer, v any) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	buf.Write(b)
	return nil
}
