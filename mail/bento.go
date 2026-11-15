package mail

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/atobeach/reef/httpclient"
)

// BentoTransport sends through Bento's transactional email API.
type BentoTransport struct {
	client   *httpclient.Client
	siteUUID string
}

// BentoConfig configures the Bento transport.
type BentoConfig struct {
	PublishableKey string
	SecretKey      string
	SiteUUID       string
	// Mock answers Bento's API instead of the network (tests).
	Mock *httpclient.MockClient
}

// NewBentoTransport authenticates with the publishable and secret keys.
func NewBentoTransport(config BentoConfig) *BentoTransport {
	client := httpclient.New(httpclient.Connector{
		BaseURL: "https://app.bentonow.com",
		Headers: map[string]string{"Authorization": basicAuth(config.PublishableKey, config.SecretKey)},
		Timeout: 30 * time.Second,
	})
	if config.Mock != nil {
		client = client.WithMock(config.Mock)
	}

	return &BentoTransport{client: client, siteUUID: config.SiteUUID}
}

// SendBentoEmails is POST /api/v1/batch/emails.
type SendBentoEmails struct {
	SiteUUID string
	Email    map[string]any
}

var (
	_ httpclient.WithQuery = SendBentoEmails{}
	_ httpclient.WithBody  = SendBentoEmails{}
)

// Method implements httpclient.Request.
func (SendBentoEmails) Method() string { return "POST" }

// Endpoint implements httpclient.Request.
func (SendBentoEmails) Endpoint() string { return "/api/v1/batch/emails" }

// Query carries the site.
func (r SendBentoEmails) Query() url.Values { return url.Values{"site_uuid": {r.SiteUUID}} }

// Body is the batch payload.
func (r SendBentoEmails) Body() any { return map[string]any{"emails": []any{r.Email}} }

// Send delivers one message.
func (b *BentoTransport) Send(ctx context.Context, message Message) error {
	res, err := b.client.Send(ctx, SendBentoEmails{SiteUUID: b.siteUUID, Email: map[string]any{
		"from":          message.From,
		"to":            strings.Join(message.To, ","),
		"subject":       message.Subject,
		"html_body":     message.HTML,
		"transactional": true,
	}})
	if err != nil {
		return fmt.Errorf("mail: bento: %w", err)
	}

	if res.Status == http.StatusUnauthorized {
		return errors.New("mail: bento rejected the credentials (401): check BENTO_PUBLISHABLE_KEY and BENTO_SECRET_KEY")
	}

	return res.Err()
}

func basicAuth(user string, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}
