package validation

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// expand turns `passengers.*.first_name` into one attribute per element that
// exists (`passengers.0.first_name`, ...). A leaf may be absent, so
// `required` still reports it; a missing container expands to nothing.
func expand(input map[string]any, attribute string) []string {
	if !strings.Contains(attribute, "*") {
		return []string{attribute}
	}

	var walk func(node any, prefix []string, rest []string) []string
	walk = func(node any, prefix []string, rest []string) []string {
		if len(rest) == 0 {
			return []string{strings.Join(prefix, ".")}
		}

		head := rest[0]
		if head != "*" {
			next, _ := child(node, head)

			return walk(next, append(slices.Clone(prefix), head), rest[1:])
		}

		var out []string
		for _, key := range keysOf(node) {
			next, _ := child(node, key)
			out = append(out, walk(next, append(slices.Clone(prefix), key), rest[1:])...)
		}

		return out
	}

	return walk(input, nil, strings.Split(attribute, "."))
}

// resolveSibling points a wildcard rule's reference at the same element:
// in `passengers.3.x`, a reference to `passengers.*.y` means `passengers.3.y`.
func resolveSibling(attribute string, pattern string, other string) string {
	if !strings.Contains(other, "*") {
		return other
	}

	concrete := strings.Split(attribute, ".")
	wild := strings.Split(pattern, ".")
	parts := strings.Split(other, ".")
	for i := range parts {
		if parts[i] == "*" && i < len(wild) && wild[i] == "*" && i < len(concrete) {
			parts[i] = concrete[i]
		}
	}

	return strings.Join(parts, ".")
}

func lookup(input map[string]any, path string) (any, bool) {
	var node any = input
	for segment := range strings.SplitSeq(path, ".") {
		next, ok := child(node, segment)
		if !ok {
			return nil, false
		}
		node = next
	}

	return node, true
}

func child(node any, key string) (any, bool) {
	switch n := node.(type) {
	case map[string]any:
		value, ok := n[key]
		return value, ok
	case []any:
		index, err := strconv.Atoi(key)
		if err != nil || index < 0 || index >= len(n) {
			return nil, false
		}
		return n[index], true
	}

	return nil, false
}

func keysOf(node any) []string {
	switch n := node.(type) {
	case map[string]any:
		return slices.Sorted(maps.Keys(n))
	case []any:
		keys := make([]string, len(n))
		for i := range n {
			keys[i] = strconv.Itoa(i)
		}
		return keys
	}

	return nil
}
