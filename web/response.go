package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
)

// Response is a fully buffered HTTP response; a middleware gets one from
// ToResponse and can still change it, since nothing is written until the
// whole pipeline has returned.
type Response struct {
	status  int
	header  http.Header
	body    []byte
	err     error
	stream  func(s *StreamWriter) error
	cookies []*http.Cookie
}

// JSON encodes value as `application/json` now, so a failure is a 500, not a
// truncated body.
func JSON(value any) *Response {
	body, err := json.Marshal(value)

	res := &Response{status: http.StatusOK, header: http.Header{}, body: body, err: err}
	res.header.Set("Content-Type", "application/json")

	return res
}

// errNoResponse is an outcome holding neither a value nor an error: a bug in
// the action or middleware, never an empty 200.
var errNoResponse = errors.New("web: the route answered neither a response nor an error")

// ToResponse is an outcome as a *Response a middleware can decorate: a value
// as the kernel would send it (a *Response as built, anything else, a string
// included, as JSON), or, beside an error, a response holding only the error's
// status. The error itself is not rendered: the kernel renders it at its edge
// and sends the headers and cookies put on that response with it. A value that
// fails to encode comes back as that error, and an outcome of nothing (nil, or
// a nil *Response) as a 500.
//
//	res, err := web.ToResponse(next(c))
//	return res.WithHeader("Cache-Control", "no-store"), err
func ToResponse(value any, err error) (*Response, error) {
	if err != nil {
		// A layer further in already decorated this error's response.
		if carrier, ok := value.(*Response); ok && carrier != nil {
			return carrier, err
		}

		return &Response{status: statusOf(err), header: http.Header{}}, err
	}

	var res *Response
	switch v := value.(type) {
	case nil:
		return serverError(), errNoResponse
	case *Response:
		if v == nil {
			return serverError(), errNoResponse
		}

		res = v
	default:
		res = JSON(v)
	}

	if res.err != nil {
		return serverError(), fmt.Errorf("web: encoding response: %w", res.err)
	}

	return res, nil
}

// serverError carries an unexpected error's 500 for middleware to decorate.
func serverError() *Response {
	return &Response{status: http.StatusInternalServerError, header: http.Header{}}
}

// Bytes sends a raw body with a content type.
func Bytes(body []byte, contentType string) *Response {
	res := &Response{status: http.StatusOK, header: http.Header{}, body: body}
	res.header.Set("Content-Type", contentType)

	return res
}

// HTML sends a string as `text/html`; an action answering a plain string
// sends JSON, so HTML is always this explicit response.
func HTML(body string) *Response {
	return Bytes([]byte(body), "text/html; charset=UTF-8")
}

// NoContent is a bodiless 204.
func NoContent() *Response {
	return &Response{status: http.StatusNoContent, header: http.Header{}}
}

// Redirect is a 302 to location with a small HTML body linking to it, for
// clients that don't follow the Location header; WithStatus picks another
// redirect status.
func Redirect(location string) *Response {
	escaped := html.EscapeString(location)
	body := `<!DOCTYPE html>
<html>
    <head>
        <meta charset="UTF-8" />
        <meta http-equiv="refresh" content="0;url='` + escaped + `'" />

        <title>Redirecting to ` + escaped + `</title>
    </head>
    <body>
        Redirecting to <a href="` + escaped + `">` + escaped + `</a>.
    </body>
</html>`

	res := HTML(body).WithStatus(http.StatusFound)
	res.header.Set("Location", location)

	return res
}

// WithStatus sets the status code.
func (r *Response) WithStatus(status int) *Response {
	r.status = status

	return r
}

// WithHeader sets a header, replacing any existing value.
func (r *Response) WithHeader(name string, value string) *Response {
	r.header.Set(name, value)

	return r
}

// WithCookie attaches a cookie, which the cookies middleware encrypts unless
// its name is excluded.
func (r *Response) WithCookie(cookie *http.Cookie) *Response {
	r.cookies = append(r.cookies, cookie)

	return r
}

// Cookies are the attached cookies, for middleware to inspect or rewrite.
func (r *Response) Cookies() []*http.Cookie {
	return r.cookies
}

// Status is the code the response goes out with, for middleware that acts
// on it.
func (r *Response) Status() int {
	return r.status
}

// Header is a header's value, "" when it is unset.
func (r *Response) Header(name string) string {
	return r.header.Get(name)
}

// Body is the buffered body, for middleware that reads it; a stream has
// none.
func (r *Response) Body() []byte {
	return r.body
}

// write sends a buffered response.
func (r *Response) write(w http.ResponseWriter) {
	r.copyHeaders(w)
	if r.body != nil {
		w.Header().Set("Content-Length", strconv.Itoa(len(r.body)))
	}

	w.WriteHeader(r.status)
	_, _ = w.Write(r.body)
}

// copyHeaders moves the headers and cookies onto w, before the status line.
func (r *Response) copyHeaders(w http.ResponseWriter) {
	for name, values := range r.header {
		w.Header()[name] = values
	}

	for _, cookie := range r.cookies {
		http.SetCookie(w, cookie)
	}
}
