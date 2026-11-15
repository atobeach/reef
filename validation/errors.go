package validation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// Errors is the message bag: messages per attribute, in insertion order.
type Errors struct {
	keys     []string
	messages map[string][]string
}

// Add appends a message for an attribute. A message already recorded for the
// attribute is dropped, so a custom rule repeating "must be a string." after
// the `string` rule shows once.
func (e *Errors) Add(attribute string, message string) {
	if e.messages == nil {
		e.messages = map[string][]string{}
	}

	if _, seen := e.messages[attribute]; !seen {
		e.keys = append(e.keys, attribute)
	}

	if slices.Contains(e.messages[attribute], message) {
		return
	}

	e.messages[attribute] = append(e.messages[attribute], message)
}

// Has reports whether the attribute has a message.
func (e *Errors) Has(attribute string) bool {
	return len(e.messages[attribute]) > 0
}

// HasAny reports whether any of the attributes has a message.
func (e *Errors) HasAny(attributes ...string) bool {
	return slices.ContainsFunc(attributes, e.Has)
}

// Keys are the attributes with messages, in the order they failed: a copy,
// so changing it leaves the bag alone.
func (e *Errors) Keys() []string {
	return slices.Clone(e.keys)
}

// Get returns an attribute's messages.
func (e *Errors) Get(attribute string) []string {
	return e.messages[attribute]
}

// Count is the total number of messages.
func (e *Errors) Count() int {
	total := 0
	for _, list := range e.messages {
		total += len(list)
	}

	return total
}

// Summary is the 422's top-level message: the first error, plus
// "(and N more error[s])".
func (e *Errors) Summary() string {
	if len(e.keys) == 0 {
		return ""
	}

	first := e.messages[e.keys[0]][0]
	more := e.Count() - 1
	switch {
	case more == 1:
		return first + " (and 1 more error)"
	case more > 1:
		return fmt.Sprintf("%s (and %d more errors)", first, more)
	}

	return first
}

// MarshalJSON keeps insertion order, unlike a Go map.
func (e *Errors) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, key := range e.keys {
		if i > 0 {
			out.WriteByte(',')
		}

		name, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}

		list, err := json.Marshal(e.messages[key])
		if err != nil {
			return nil, err
		}

		out.Write(name)
		out.WriteByte(':')
		out.Write(list)
	}
	out.WriteByte('}')

	return out.Bytes(), nil
}
