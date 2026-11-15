package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/panics"
)

// Error is an expected HTTP failure: rendered with its own status, message
// and headers, never reported.
type Error struct {
	Code    int
	Message string
	Headers map[string]string
}

func (e *Error) Error() string {
	return e.Message
}

// HTTPStatus is the status the kernel renders.
func (e *Error) HTTPStatus() int {
	return e.Code
}

// Abort ends the request with code and message. Return it from a controller
// or middleware.
func Abort(code int, message string) error {
	return &Error{Code: code, Message: message}
}

// NotFound is a 404 with an empty message (`{"message": ""}`).
func NotFound() error {
	return &Error{Code: http.StatusNotFound}
}

// TooManyRequests is the throttle's 429, with the retry and rate-limit
// headers a client reads to back off.
func TooManyRequests(ctx context.Context, limit int, retryAfter time.Duration) error {
	seconds := int(retryAfter.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}

	return &Error{
		Code:    http.StatusTooManyRequests,
		Message: "Too Many Attempts.",
		Headers: map[string]string{
			"Retry-After":           strconv.Itoa(seconds),
			"X-RateLimit-Reset":     strconv.FormatInt(clock.Now(ctx).Add(time.Duration(seconds)*time.Second).Unix(), 10),
			"X-RateLimit-Limit":     strconv.Itoa(limit),
			"X-RateLimit-Remaining": "0",
		},
	}
}

// statusCoder is any error that knows its status (db.ModelNotFoundError
// included), so packages define renderable errors without importing web.
type statusCoder interface {
	error
	HTTPStatus() int
}

// isExpected reports an error that renders with its own status: an outcome,
// never logged or reported. A panic is never one, whatever it panicked with.
func isExpected(err error) bool {
	_, ok := errors.AsType[statusCoder](err)

	return ok
}

// statusOf is the status err renders with: its own, or 500.
func statusOf(err error) int {
	if coder, ok := errors.AsType[statusCoder](err); ok {
		return coder.HTTPStatus()
	}

	return http.StatusInternalServerError
}

// renderError turns an error into its JSON body, `{"message": ...}` (or the
// error's own HTTPBody), with "Server Error" hiding the details of anything
// unexpected unless APP_DEBUG is on.
func (k *Kernel) renderError(c *Context, err error) *Response {
	if coder, ok := errors.AsType[statusCoder](err); ok {
		// The coder's own message, never err.Error(): a wrapping
		// fmt.Errorf("loading guide: %w", ...) is internal context.
		var body any = map[string]string{"message": coder.Error()}
		if custom, ok := coder.(interface{ HTTPBody() any }); ok {
			body = custom.HTTPBody()
		}

		res := JSON(body)
		if res.err == nil {
			res.WithStatus(coder.HTTPStatus())
			if httpErr, ok := errors.AsType[*Error](err); ok {
				for name, value := range httpErr.Headers {
					res.WithHeader(name, value)
				}
			}

			return res
		}

		// A body with no JSON form is the server's failure, not the outcome
		// the coder meant: never its status with an empty body.
		err = fmt.Errorf("web: encoding the error body of %T: %w", coder, res.err)
	}

	k.unexpected(c, err)

	message := "Server Error"
	if k.debug {
		message = err.Error()
	}

	return JSON(map[string]string{"message": message}).WithStatus(http.StatusInternalServerError)
}

// unexpected logs err, with the stack when it holds a panic, then hands it
// to Report. Callers decide what is unexpected.
func (k *Kernel) unexpected(c *Context, err error) {
	attributes := []any{"method", c.Method(), "path", c.Path(), "error", err}
	if recovered, ok := errors.AsType[*panics.Error](err); ok {
		attributes = append(attributes, "stack", recovered.Stack())
	}

	k.logger.ErrorContext(c, "unhandled error", attributes...)

	if k.report == nil {
		return
	}

	// Reporting must never turn one failure into two.
	defer func() { _ = recover() }()
	k.report(c, err)
}
