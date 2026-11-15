package httpclient

import (
	"bytes"
	"context"
	"io"
	"time"
)

// Stream sends req once (a streamed body cannot be replayed) and hands back
// the body unread, for the caller to close. A failed response is read whole
// with a nil body, so it maps like any other failure. Hooks run on headers.
func (c *Client) Stream(ctx context.Context, req Request) (res *Response, body io.ReadCloser, err error) {
	started := time.Now()
	res, body, err = c.streamAttempt(ctx, req)

	for _, hook := range c.connector.Hooks {
		hook(ctx, req, res, err, time.Since(started))
	}

	return res, body, err
}

// streamAttempt sends req, keeping a successful body open for the caller.
func (c *Client) streamAttempt(ctx context.Context, req Request) (*Response, io.ReadCloser, error) {
	httpReq, sent, err := c.build(ctx, req)
	if err != nil {
		return nil, nil, err
	}

	if c.mock != nil {
		res, err := c.mock.respond(c, req, httpReq, sent)
		if err != nil || res.Failed() {
			return res, nil, err
		}

		return res, io.NopCloser(bytes.NewReader(res.body)), nil
	}

	httpRes, err := c.http.Do(httpReq)
	if err != nil {
		return nil, nil, err
	}

	res := c.response(httpRes.StatusCode, httpRes.Header, nil, req)
	if res.Failed() {
		defer httpRes.Body.Close()

		raw, err := io.ReadAll(httpRes.Body)
		if err != nil {
			return nil, nil, err
		}
		res.body = raw

		return res, nil, nil
	}

	return res, httpRes.Body, nil
}
