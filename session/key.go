package session

import (
	"encoding/json"
	"strconv"
)

// Key names a session attribute holding a T; a dot nests (`login.id`), as
// stored sessions do. It is the one way to read and write an attribute:
//
//	const oauthState session.Key[string] = "state"
//
//	oauthState.Put(s, state)
//	stored, ok := oauthState.Pull(s)
type Key[T any] string

// Get reads the attribute; ok is false when it is absent, null or not a T.
// A value put this request reads as one decoded from the store does: both go
// through JSON into T. A Key[int64] also reads a numeric string and
// truncates a fraction; a Key[any] reads the attribute as stored.
func (k Key[T]) Get(s *Session) (T, bool) {
	var value T

	stored := s.get(string(k))
	if stored == nil {
		return value, false
	}

	switch target := any(&value).(type) {
	case *any:
		*target = stored

		return value, true
	case *int64:
		n, ok := toInt64(stored)
		if ok {
			*target = n
		}

		return value, ok
	}

	raw, err := json.Marshal(stored)
	if err != nil {
		return value, false
	}

	var decoded T
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return value, false
	}

	return decoded, true
}

// Put sets the attribute; the session's save encodes it as JSON.
func (k Key[T]) Put(s *Session, value T) {
	s.put(string(k), value)
}

// Pull reads the attribute, as Get does, and removes it.
func (k Key[T]) Pull(s *Session) (T, bool) {
	value, ok := k.Get(s)
	k.Forget(s)

	return value, ok
}

// Forget removes the attribute: an exact top-level key first, else the
// dotted path's leaf.
func (k Key[T]) Forget(s *Session) {
	s.forget(string(k))
}

// Flash sets the attribute for the next request only.
func (k Key[T]) Flash(s *Session, value T) {
	k.Put(s, value)
	s.flashNext(string(k))
}

// toInt64 reads an integer however the session holds it: put this request
// (an int, an int64 or a float64), decoded from the store (a json.Number),
// or a numeric string; a fraction is truncated.
func toInt64(stored any) (int64, bool) {
	switch v := stored.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n, true
		}

		f, err := v.Float64()

		return int64(f), err == nil
	case string:
		n, err := strconv.ParseInt(v, 10, 64)

		return n, err == nil
	}

	return 0, false
}
