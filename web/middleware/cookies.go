package middleware

import (
	"net/url"
	"slices"

	"github.com/atobeach/reef/cookie"
	"github.com/atobeach/reef/crypt"
	"github.com/atobeach/reef/support"
	"github.com/atobeach/reef/web"
)

// Cookies seals the app's cookies: incoming ones are decrypted (an invalid
// one reads as absent), queued and outgoing ones encrypted, except the names
// in except, which travel raw (a cookie set before encryption was on).
func Cookies(encrypter *crypt.Encrypter, except ...string) web.Middleware {
	return func(c *web.Context, next web.Handler) (any, error) {
		decrypted := map[string]string{}
		for _, incoming := range c.RawCookies() {
			// Values go out url-encoded (RawURLEncode), as earlier-issued
			// cookies were; '+' decodes as a space.
			raw, err := url.QueryUnescape(incoming.Value)
			if err != nil {
				continue
			}

			if slices.Contains(except, incoming.Name) {
				decrypted[incoming.Name] = raw

				continue
			}

			if value, ok := cookie.Decrypt(encrypter, incoming.Name, raw); ok {
				decrypted[incoming.Name] = value
			}
		}
		c.SetCookies(decrypted)

		res, err := web.ToResponse(next(c))

		for _, queued := range c.QueuedCookies() {
			res.WithCookie(queued)
		}

		for _, outgoing := range res.Cookies() {
			if !slices.Contains(except, outgoing.Name) && outgoing.MaxAge >= 0 {
				sealed, sealErr := cookie.Encrypt(encrypter, outgoing.Name, outgoing.Value)
				if sealErr != nil {
					// The response is dropped with its cookies: none goes out
					// unsealed.
					return nil, sealErr
				}
				outgoing.Value = sealed
			}

			outgoing.Value = support.RawURLEncode(outgoing.Value)
		}

		return res, err
	}
}
