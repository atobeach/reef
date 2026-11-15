// Package session is the database-backed session, in the format stored
// sessions have: base64(JSON) payloads keyed by a 40-character id carried in
// the encrypted session cookie.
package session

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/atobeach/reef/support"
)

// Session is one request's view of the session.
type Session struct {
	id         string
	attributes map[string]any
	// previousID is set by Regenerate(true) so the old row is destroyed.
	previousID string
	// exists reports a row with this id found when the request began.
	exists bool
}

// newSession starts an empty session with a fresh id and CSRF token.
func newSession() *Session {
	s := &Session{id: support.RandomString(40), attributes: map[string]any{}}
	s.RegenerateToken()

	return s
}

// Decode is the session a stored payload holds, for reading or seeding one
// outside a request; an empty payload is an empty session.
func Decode(id string, payload string) (*Session, error) {
	s := &Session{id: id, attributes: map[string]any{}}
	if payload == "" {
		return s, nil
	}

	attributes, err := decode(payload)
	if err != nil {
		return nil, fmt.Errorf("session: decoding %s: %w", id, err)
	}
	s.attributes = attributes

	return s, nil
}

// Encode is the payload the session is stored as.
func (s *Session) Encode() (string, error) { return encode(s.attributes) }

// ID is the session id.
func (s *Session) ID() string { return s.id }

// get reads an attribute; a dot nests (`login.id`), as stored sessions do.
func (s *Session) get(key string) any { return getDotted(s.attributes, key) }

// put sets an attribute (a dot nests).
func (s *Session) put(key string, value any) { setDotted(s.attributes, key, value) }

// forget removes an attribute.
func (s *Session) forget(key string) { forgetDotted(s.attributes, key) }

// RegenerateWithToken moves to a new id AND a new CSRF token, so neither a
// fixated id nor an old token survives a sign-in.
func (s *Session) RegenerateWithToken(destroy bool) {
	s.Regenerate(destroy)
	s.RegenerateToken()
}

// passwordConfirmedAt holds the unix time the password was last confirmed.
const passwordConfirmedAt Key[int64] = "auth.password_confirmed_at"

// PasswordConfirmed stamps `auth.password_confirmed_at`, which
// auth.RequirePassword reads.
func (s *Session) PasswordConfirmed(now time.Time) {
	passwordConfirmedAt.Put(s, now.Unix())
}

// PasswordConfirmedAt is when the password was last confirmed; zero when
// never.
func (s *Session) PasswordConfirmedAt() time.Time {
	seconds, ok := passwordConfirmedAt.Get(s)
	if !ok {
		return time.Time{}
	}

	return time.Unix(seconds, 0).UTC()
}

// getDotted reads key: an exact top-level key wins, then the dotted path.
func getDotted(attributes map[string]any, key string) any {
	if value, ok := attributes[key]; ok {
		return value
	}

	if !strings.Contains(key, ".") {
		return nil
	}

	var node any = attributes
	for segment := range strings.SplitSeq(key, ".") {
		object, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = object[segment]
	}

	return node
}

// setDotted writes key: every dot nests, replacing non-object intermediates.
func setDotted(attributes map[string]any, key string, value any) {
	segments := strings.Split(key, ".")
	node := attributes
	for _, segment := range segments[:len(segments)-1] {
		next, ok := node[segment].(map[string]any)
		if !ok {
			next = map[string]any{}
			node[segment] = next
		}
		node = next
	}

	node[segments[len(segments)-1]] = value
}

// forgetDotted removes an exact top-level key, else the dotted path.
func forgetDotted(attributes map[string]any, key string) {
	if _, ok := attributes[key]; ok {
		delete(attributes, key)

		return
	}

	segments := strings.Split(key, ".")
	node := attributes
	for _, segment := range segments[:len(segments)-1] {
		next, ok := node[segment].(map[string]any)
		if !ok {
			return
		}
		node = next
	}

	delete(node, segments[len(segments)-1])
}

// Token is the CSRF token (`_token`).
func (s *Session) Token() string {
	token, _ := s.attributes["_token"].(string)

	return token
}

// RegenerateToken mints a new CSRF token.
func (s *Session) RegenerateToken() {
	s.attributes["_token"] = support.RandomString(40)
}

// Regenerate moves the session to a new id, against fixation; with destroy
// the old row is deleted on save.
func (s *Session) Regenerate(destroy bool) {
	// Only the arriving id has a row: destroying an intermediate one would
	// leave the signed-in original alive.
	if destroy && s.previousID == "" {
		s.previousID = s.id
	}

	s.id = support.RandomString(40)
	s.exists = false
}

// Invalidate empties the session and moves it to a new id (logout).
func (s *Session) Invalidate() {
	s.attributes = map[string]any{}
	s.Regenerate(true)
	s.RegenerateToken()
}

// flashNext keeps key's value for the next request only.
func (s *Session) flashNext(key string) {
	flash := s.flash()
	flash["new"] = appendUnique(flash["new"], key)
	flash["old"] = slices.DeleteFunc(slices.Clone(flash["old"]), func(k string) bool { return k == key })
	s.setFlash(flash)
}

// Reflash keeps last request's flashed values for one more request.
func (s *Session) Reflash() {
	flash := s.flash()
	for _, key := range flash["old"] {
		flash["new"] = appendUnique(flash["new"], key)
	}

	s.setFlash(map[string][]string{"old": {}, "new": flash["new"]})
}

// ageFlash drops last request's flashed values and ages this request's,
// on every save.
func (s *Session) ageFlash() {
	flash := s.flash()
	for _, key := range flash["old"] {
		s.forget(key)
	}

	s.setFlash(map[string][]string{"old": flash["new"], "new": {}})
}

func (s *Session) flash() map[string][]string {
	out := map[string][]string{"old": {}, "new": {}}

	raw, _ := s.attributes["_flash"].(map[string]any)
	for _, bucket := range []string{"old", "new"} {
		list, _ := raw[bucket].([]any)
		for _, item := range list {
			if key, ok := item.(string); ok {
				out[bucket] = append(out[bucket], key)
			}
		}
	}

	return out
}

func (s *Session) setFlash(flash map[string][]string) {
	encoded := map[string]any{}
	for bucket, keys := range flash {
		list := make([]any, len(keys))
		for i, key := range keys {
			list[i] = key
		}
		encoded[bucket] = list
	}

	s.attributes["_flash"] = encoded
}

// SetPreviousURL records `_previous` as StartSession does for page GETs.
func (s *Session) SetPreviousURL(url string, route string) {
	var name any
	if route != "" {
		name = route
	}

	s.attributes["_previous"] = map[string]any{"url": url, "route": name}
}

// All returns a copy of the attributes.
func (s *Session) All() map[string]any {
	return maps.Clone(s.attributes)
}

// validID reports a well-formed id: exactly 40 ASCII letters and digits.
func validID(id string) bool {
	if len(id) != 40 {
		return false
	}

	for _, r := range id {
		if r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r)) {
			return false
		}
	}

	return true
}

// encode is the stored payload: base64 of the JSON attributes.
func encode(attributes map[string]any) (string, error) {
	raw, err := json.Marshal(attributes)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(raw), nil
}

// decode reads a stored payload. Numbers stay json.Number, so an int user id
// reads back as an integer.
func decode(payload string) (map[string]any, error) {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var attributes map[string]any
	if err := decoder.Decode(&attributes); err != nil {
		return nil, err
	}

	if attributes == nil {
		attributes = map[string]any{}
	}

	return attributes, nil
}

func appendUnique(list []string, key string) []string {
	if slices.Contains(list, key) {
		return list
	}

	return append(list, key)
}
