package support

import (
	"net/url"
	"strings"
)

// RawURLEncode is RFC 3986 percent-encoding: only A-Za-z0-9-_.~ stay, every
// other byte (spaces included) is %XX.
func RawURLEncode(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

// FormEncode escapes a query value form-style: everything but A-Za-z0-9_.-
// is %XX, and spaces become '+'.
func FormEncode(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "~", "%7E")
}
