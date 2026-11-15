package ai

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ProviderErrorKind names a provider failure that fails every turn alike.
type ProviderErrorKind string

// The failoverable provider failures: every turn fails the same way until
// the provider recovers.
const (
	InsufficientCredits ProviderErrorKind = "insufficient_credits"
	RateLimited         ProviderErrorKind = "rate_limited"
	ProviderOverloaded  ProviderErrorKind = "overloaded"
	ProviderConnection  ProviderErrorKind = "connection"
)

// ProviderError is a provider out of credits, rate limited, overloaded or
// unreachable: a client shows it as an outage rather than a broken reply.
type ProviderError struct {
	Kind     ProviderErrorKind
	Provider string
	Status   int
	Err      error
}

func (e *ProviderError) Error() string {
	switch e.Kind {
	case InsufficientCredits:
		return fmt.Sprintf("AI provider [%s] has insufficient credits or quota.", e.Provider)
	case RateLimited:
		return fmt.Sprintf("Application rate limited by AI provider [%s].", e.Provider)
	case ProviderOverloaded:
		return fmt.Sprintf("AI provider [%s] is overloaded.", e.Provider)
	default:
		return fmt.Sprintf("Could not connect to AI provider [%s].", e.Provider)
	}
}

func (e *ProviderError) Unwrap() error { return e.Err }

// IsFailoverable reports whether err is (or wraps) a ProviderError.
func IsFailoverable(err error) bool {
	var providerErr *ProviderError

	return errors.As(err, &providerErr)
}

// RequestError is any other failed provider status. Error() carries only
// the status: the body can echo the prompt, and error messages reach error
// tracking.
type RequestError struct {
	Status int
	Body   []byte
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("HTTP request returned status code %d", e.Status)
}

// StreamError is a step the provider failed inside the stream, which the
// stream already surfaced as an ErrorEvent.
type StreamError struct {
	Event *ErrorEvent
}

func (e *StreamError) Error() string {
	if e.Event != nil {
		return e.Event.Message
	}

	return "The provider ended the stream without completing the step."
}

// IsStreamError reports whether err is (or wraps) a StreamError.
func IsStreamError(err error) bool {
	var streamErr *StreamError

	return errors.As(err, &streamErr)
}

// NoSuchToolError is a call to a tool the step did not offer, from an agent
// that does not repair tool calls.
type NoSuchToolError struct {
	Name string
}

func (e *NoSuchToolError) Error() string {
	return fmt.Sprintf("Model tried to call unavailable tool '%s'.", e.Name)
}

// ResponseError is an unusable provider answer (or a middleware that
// answered a step with nothing). Message is what the run reports; Err, when
// set, is why the answer was unusable (a decode failure, say).
type ResponseError struct {
	Message string
	Err     error
}

func (e *ResponseError) Error() string {
	return e.Message
}

func (e *ResponseError) Unwrap() error { return e.Err }

// TurnDeadlineError is a streamed run stopped by
// StreamableAgentResponse.Deadline. It unwraps to context.DeadlineExceeded,
// so the cut-off request is not mistaken for a provider outage.
type TurnDeadlineError struct {
	Deadline time.Duration
}

func (e *TurnDeadlineError) Error() string {
	return fmt.Sprintf("The run exceeded its %s deadline.", e.Deadline)
}

func (e *TurnDeadlineError) Unwrap() error { return context.DeadlineExceeded }
