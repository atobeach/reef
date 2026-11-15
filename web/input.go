package web

import (
	"bytes"
	"encoding/json"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/atobeach/reef/validation"
)

// Bracketed-key limits, so one `a[][][]…` body can't overflow the stack: a
// key nested deeper than maxInputNesting is dropped, and parsing stops after
// maxInputVars variables.
const (
	maxInputNesting = 64
	maxInputVars    = 1000
)

// untrimmed are the fields whose surrounding whitespace is significant.
var untrimmed = map[string]bool{"current_password": true, "password": true, "password_confirmation": true}

// All is the request input: the query string merged with a JSON or form
// body (body keys win), strings trimmed (passwords excepted) and blanks nil,
// so a blank field fails `required`. Unreadable input (a 413 body, a
// malformed query's 400) is an error every call returns, never a partial.
func (c *Context) All() (validation.Input, error) {
	if c.input == nil && c.inputErr == nil {
		c.input, c.inputErr = c.parseInput()
	}

	return c.input, c.inputErr
}

// Input is the input at a dotted key, nil when absent or unreadable.
func (c *Context) Input(key string) any {
	return c.all().Value(key)
}

// Has reports whether every key is present, even if null.
func (c *Context) Has(keys ...string) bool {
	return c.all().Has(keys...)
}

// Filled reports a key that is present and not blank.
func (c *Context) Filled(key string) bool {
	return c.all().Filled(key)
}

// String is a scalar input as text ("1" for true), "" otherwise.
func (c *Context) String(key string) string {
	return c.all().String(key)
}

// Boolean reads a checkbox-style flag: true for true, 1, "1", "true", "on",
// "yes" (any case); false otherwise.
func (c *Context) Boolean(key string) bool {
	return c.all().Boolean(key)
}

// Array is the list at key: a list, or an object keyed "0".."n-1".
func (c *Context) Array(key string) ([]any, bool) {
	return c.all().Array(key)
}

// Merge adds values to the input, replacing the keys it already has, so
// validation and every later read see them. Unreadable input stays
// unreadable: All still returns its error.
func (c *Context) Merge(values map[string]any) {
	if input, err := c.All(); err == nil {
		maps.Copy(input, values)
	}
}

// all is All for the readers, which read unreadable input as empty; the
// error waits for the next All (Validate's).
func (c *Context) all() validation.Input {
	input, _ := c.All()

	return input
}

func (c *Context) parseInput() (validation.Input, error) {
	input, err := parseQuery(c.request.URL.RawQuery)
	if err != nil {
		return nil, Abort(http.StatusBadRequest, "Malformed query string.")
	}

	body, err := c.parseBody()
	if err != nil {
		return nil, err
	}

	maps.Copy(input, body)

	return normalize(input, "").(map[string]any), nil
}

func (c *Context) parseBody() (map[string]any, error) {
	if c.request.Method == http.MethodGet || c.request.Method == http.MethodHead {
		return map[string]any{}, nil
	}

	raw, err := c.Body()
	if err != nil {
		return nil, err
	}

	mediaType, _, _ := mime.ParseMediaType(c.Header("Content-Type"))

	switch {
	case strings.HasSuffix(mediaType, "json"):
		return decodeJSONObject(raw), nil
	case mediaType == "application/x-www-form-urlencoded":
		return parseQuery(string(raw))
	default:
		return map[string]any{}, nil
	}
}

// decodeJSONObject reads a JSON body. Anything but an object — blank,
// malformed, a bare array — is empty input, so validation reports the
// missing fields instead of the request failing on the body.
func decodeJSONObject(raw []byte) map[string]any {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var decoded any
	if decoder.Decode(&decoded) != nil {
		return map[string]any{}
	}

	if object, ok := decoded.(map[string]any); ok {
		return object
	}

	return map[string]any{}
}

// parseQuery parses a query string or form body with bracketed keys
// (`a[]=1&a[]=2`, `a[b][c]=x`) into nested maps and lists. Dots and spaces in
// a top-level name become underscores.
func parseQuery(raw string) (map[string]any, error) {
	root := map[string]any{}
	if raw == "" {
		return root, nil
	}

	variables := 0
	for pair := range strings.SplitSeq(raw, "&") {
		if pair == "" {
			continue
		}

		variables++
		if variables > maxInputVars {
			break
		}

		rawKey, rawValue, _ := strings.Cut(pair, "=")
		key, err := url.QueryUnescape(rawKey)
		if err != nil {
			return nil, err
		}

		value, err := url.QueryUnescape(rawValue)
		if err != nil {
			return nil, err
		}

		name, path, ok := splitBrackets(key)
		if name == "" || !ok {
			continue
		}

		insert(root, append([]string{topLevelName(name)}, path...), value)
	}

	// The input itself stays an object, even when its keys are 0..n-1.
	for key, value := range root {
		root[key] = listify(value)
	}

	return root, nil
}

// splitBrackets cuts "a[b][]" into "a" and ["b", ""]. ok is false for a key
// nested past maxInputNesting, which is dropped.
func splitBrackets(key string) (name string, path []string, ok bool) {
	open := strings.IndexByte(key, '[')
	if open <= 0 || !strings.Contains(key[open:], "]") {
		return key, nil, true
	}

	name, rest := key[:open], key[open:]
	for strings.HasPrefix(rest, "[") {
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			break
		}

		if len(path) == maxInputNesting {
			return name, nil, false
		}

		path = append(path, rest[1:end])
		rest = rest[end+1:]
	}

	return name, path, true
}

func topLevelName(name string) string {
	return strings.NewReplacer(".", "_", " ", "_").Replace(name)
}

// appendKey marks a `[]` segment until listify numbers it.
const appendKey = "\x00append"

func insert(node map[string]any, path []string, value string) {
	key := path[0]
	if key == "" {
		key = appendKey + strconv.Itoa(len(node))
	}

	if len(path) == 1 {
		node[key] = value

		return
	}

	next, ok := node[key].(map[string]any)
	if !ok {
		next = map[string]any{}
		node[key] = next
	}

	insert(next, path[1:], value)
}

// listify turns objects whose keys are 0..n-1 (or appends) into lists, so
// `a[]=1&a[]=2` and `a[0]=1&a[1]=2` both read as a list.
func listify(node any) any {
	object, ok := node.(map[string]any)
	if !ok {
		return node
	}

	for key, value := range object {
		object[key] = listify(value)
	}

	if len(object) == 0 {
		return object
	}

	keys := make([]string, 0, len(object))
	appended := true
	for key := range object {
		keys = append(keys, key)
		if !strings.HasPrefix(key, appendKey) {
			appended = false
		}
	}

	if appended {
		sort.Slice(keys, func(i, j int) bool { return appendIndex(keys[i]) < appendIndex(keys[j]) })
		list := make([]any, len(keys))
		for i, key := range keys {
			list[i] = object[key]
		}

		return list
	}

	list := make([]any, len(keys))
	for _, key := range keys {
		index, err := strconv.Atoi(key)
		if err != nil || index < 0 || index >= len(keys) || strconv.Itoa(index) != key {
			return object
		}
		list[index] = object[key]
	}

	return list
}

func appendIndex(key string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(key, appendKey))

	return n
}

// normalize trims strings (except untrimmed fields) and turns empty strings
// into nil, recursively.
func normalize(value any, key string) any {
	switch v := value.(type) {
	case string:
		if !untrimmed[key] {
			v = strings.TrimSpace(v)
		}

		if v == "" {
			return nil
		}

		return v
	case map[string]any:
		for k, item := range v {
			v[k] = normalize(item, k)
		}

		return v
	case []any:
		for i, item := range v {
			v[i] = normalize(item, key)
		}

		return v
	}

	return value
}
