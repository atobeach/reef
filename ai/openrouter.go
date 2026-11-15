package ai

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/atobeach/reef/httpclient"
	"github.com/atobeach/reef/ordered"
)

// Gateway sends one generation step to a provider.
type Gateway interface {
	// GenerateStep runs the step without streaming.
	GenerateStep(ctx context.Context, provider ProviderConfig, step PendingStep) (*StepResponse, error)
	// StreamStep runs the step streaming, emitting its events as they
	// arrive. A nil response with a nil error means the provider ended the
	// stream with an error event (already emitted).
	StreamStep(ctx context.Context, invocationID string, provider ProviderConfig, step PendingStep, emit func(StreamEvent)) (*StepResponse, error)
}

// ProviderConfig is one `ai.providers.*` config entry.
type ProviderConfig struct {
	// Name is the config key ("openrouter"): what stream events and stored
	// meta report as the provider.
	Name   string
	Driver string
	Key    string
	// URL overrides the driver's base URL.
	URL         string
	HTTPReferer string
	Title       string
	Headers     map[string]string
}

// ChatCompletion is POST chat/completions (non-streamed).
type ChatCompletion struct {
	body    *ordered.Object
	headers map[string]string
}

var (
	_ httpclient.WithBody    = ChatCompletion{}
	_ httpclient.WithHeaders = ChatCompletion{}
)

// StreamChatCompletion is POST chat/completions with `stream: true`, its own
// type so tests script streamed and plain steps apart.
type StreamChatCompletion struct {
	ChatCompletion
}

// Method implements httpclient.Request.
func (ChatCompletion) Method() string { return "POST" }

// Endpoint implements httpclient.Request.
func (ChatCompletion) Endpoint() string { return "chat/completions" }

// Body implements httpclient.WithBody.
func (r ChatCompletion) Body() any { return r.body }

// Headers implements httpclient.WithHeaders.
func (r ChatCompletion) Headers() map[string]string { return r.headers }

// OpenRouterGateway is the Gateway over OpenRouter's Chat Completions API
// (any OpenAI-compatible endpoint, given its URL).
type OpenRouterGateway struct {
	// HTTP builds the client for a connector, so a test can answer it from a
	// MockClient.
	HTTP func(connector httpclient.Connector) *httpclient.Client
	// Encode writes the request bodies and the tool arguments and results
	// inside them (nil: encoding/json); resent history matches stored bytes
	// only with the encoder they were stored with.
	Encode Encoder
}

// client authenticates with the provider key and adds OpenRouter's optional
// attribution headers. Timeouts ride the request context.
func (g OpenRouterGateway) client(provider ProviderConfig) *httpclient.Client {
	baseURL := strings.TrimRight(provider.URL, "/")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}

	headers := map[string]string{"Authorization": "Bearer " + provider.Key}
	if provider.HTTPReferer != "" {
		headers["HTTP-Referer"] = provider.HTTPReferer
	}
	if provider.Title != "" {
		headers["X-OpenRouter-Title"] = provider.Title
	}
	for name, value := range provider.Headers {
		headers[name] = value
	}

	return g.HTTP(httpclient.Connector{
		BaseURL:        baseURL,
		Headers:        headers,
		ConnectTimeout: 10 * time.Second,
		EncodeBody:     g.Encode.encode,
	})
}

// GenerateStep implements Gateway.
func (g OpenRouterGateway) GenerateStep(ctx context.Context, provider ProviderConfig, step PendingStep) (*StepResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, stepTimeout(step))
	defer cancel()

	res, err := g.client(provider).Send(ctx, ChatCompletion{body: chatCompletionBody(provider, step, g.Encode)})
	if err := failoverError(provider.Name, res, err); err != nil {
		return nil, err
	}

	return parseCompletion(res.Body(), provider, len(step.Schema) > 0)
}

// StreamStep implements Gateway.
func (g OpenRouterGateway) StreamStep(ctx context.Context, invocationID string, provider ProviderConfig, step PendingStep, emit func(StreamEvent)) (*StepResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, stepTimeout(step))
	defer cancel()

	body := chatCompletionBody(provider, step, g.Encode)
	body.Set("stream", true)
	streamOptions := ordered.NewObject()
	streamOptions.Set("include_usage", true)
	body.Set("stream_options", streamOptions)

	res, stream, err := g.client(provider).Stream(ctx, StreamChatCompletion{ChatCompletion{body: body}})
	if err := failoverError(provider.Name, res, err); err != nil {
		return nil, err
	}
	defer stream.Close()

	return processTextStream(ctx, invocationID, provider, step.Model, stream, emit)
}

func stepTimeout(step PendingStep) time.Duration {
	if step.Timeout > 0 {
		return step.Timeout
	}

	return defaultTimeout
}

// failoverError classifies a failed provider call: the statuses that fail
// every turn until the provider recovers become a ProviderError, any other
// failed status a RequestError. A cancelled request stays a cancellation.
func failoverError(provider string, res *httpclient.Response, err error) error {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}

		return &ProviderError{Kind: ProviderConnection, Provider: provider, Err: err}
	}

	if res == nil || !res.Failed() {
		return nil
	}

	switch res.Status {
	case http.StatusTooManyRequests:
		return &ProviderError{Kind: RateLimited, Provider: provider, Status: res.Status}
	case http.StatusPaymentRequired:
		return &ProviderError{Kind: InsufficientCredits, Provider: provider, Status: res.Status}
	// 520, 522 and 524 are Cloudflare's origin errors, with no stdlib names.
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 520, 522, 524:
		return &ProviderError{Kind: ProviderOverloaded, Provider: provider, Status: res.Status}
	}

	return &RequestError{Status: res.Status, Body: res.Body()}
}
