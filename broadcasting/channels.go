package broadcasting

import (
	"context"
	"regexp"
	"strings"
)

// User is whoever the auth guard resolved for the request.
type User interface {
	AuthID() int64
}

// Authorizer decides whether user may join a channel matching a pattern;
// params are the pattern's {placeholders} in order.
type Authorizer func(ctx context.Context, user User, params []string) bool

// Channels holds the private-channel authorisation rules, registered by the
// channels func Routes is given.
type Channels struct {
	routes []channelRoute
}

type channelRoute struct {
	matcher   *regexp.Regexp
	authorize Authorizer
}

var placeholder = regexp.MustCompile(`\{(.*?)\}`)

// Channel registers a rule for pattern, whose {placeholders} each match
// one dot-free segment.
func (c *Channels) Channel(pattern string, authorize Authorizer) {
	// Unescaped, a `.` matches any character: harmless in dotted names like
	// `App.Models.User.{id}`.
	expression := "^" + placeholder.ReplaceAllString(pattern, `([^\.]+)`) + "$"

	c.routes = append(c.routes, channelRoute{matcher: regexp.MustCompile(expression), authorize: authorize})
}

// normalize strips the private-/presence- prefix rules are written without.
func normalize(channel string) string {
	for _, prefix := range []string{"private-encrypted-", "private-", "presence-"} {
		if rest, ok := strings.CutPrefix(channel, prefix); ok {
			return rest
		}
	}

	return channel
}

func isGuarded(channel string) bool {
	return strings.HasPrefix(channel, "private-") || strings.HasPrefix(channel, "presence-")
}

// Allowed reports whether user may join channel: the first matching rule
// decides; no matching rule, or a guest on a guarded channel, denies.
func (c *Channels) Allowed(ctx context.Context, user User, channel string) bool {
	if channel == "" || (isGuarded(channel) && user == nil) {
		return false
	}

	name := normalize(channel)

	for _, route := range c.routes {
		match := route.matcher.FindStringSubmatch(name)
		if match == nil {
			continue
		}

		return route.authorize(ctx, user, match[1:])
	}

	return false
}
