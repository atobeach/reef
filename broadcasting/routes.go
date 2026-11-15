package broadcasting

import (
	"net/http"
	"strings"

	"github.com/atobeach/reef/session"
	"github.com/atobeach/reef/web"
)

// Routes registers GET|POST /broadcasting/auth, which signs private-channel
// subscriptions against the rules channels defines; a nil signer answers an
// empty 200. Mount it behind the session and the signed-in user: a
// signature is only as trustworthy as its user.
func Routes(r *web.Router, channels func(*Channels), signer *Pusher, user func(*web.Context) (User, error)) *web.Route {
	rules := &Channels{}
	channels(rules)

	return r.Match([]string{http.MethodGet, http.MethodPost}, "/broadcasting/auth", func(c *web.Context) (*web.Response, error) {
		if s := session.FromContext(c); s != nil {
			s.Reflash()
		}

		if signer == nil {
			return web.HTML(""), nil
		}

		return authorize(c, rules, signer, user)
	})
}

// authorize answers a private-channel subscription with its signed `auth`,
// or a 403; presence channels are refused.
func authorize(c *web.Context, channels *Channels, signer *Pusher, user func(*web.Context) (User, error)) (*web.Response, error) {
	channel, socket := c.String("channel_name"), c.String("socket_id")

	subscriber, err := user(c)
	if err != nil {
		return nil, err
	}

	if strings.HasPrefix(channel, "presence-") || !strings.HasPrefix(channel, "private") || !channels.Allowed(c, subscriber, channel) {
		return nil, web.Abort(http.StatusForbidden, "")
	}

	// A malformed socket id is a 500, not a 403: pusher-js never sends one.
	auth, err := signer.AuthorizeChannel(channel, socket)
	if err != nil {
		return nil, err
	}

	return web.JSON(auth), nil
}
