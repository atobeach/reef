package realtime

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
)

// pipeListener serves connections dialed through it over net.Pipe, so a
// server runs inside a synctest bubble. A pipe has no buffer: a write waits
// until the client reads it. With stall set, the server's every write
// returns that long after the client read it, as a writer descheduled right
// after its syscall does.
type pipeListener struct {
	conns     chan net.Conn
	closed    chan struct{}
	closeOnce sync.Once
	stall     time.Duration
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		if l.stall > 0 {
			return stalledConn{Conn: conn, stall: l.stall}, nil
		}

		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })

	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

// dial is a Transport's DialContext: it hands the server end to Accept and
// returns the client end.
func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	server, client := net.Pipe()

	select {
	case l.conns <- server:
		return client, nil
	case <-l.closed:
		_, _ = server.Close(), client.Close()

		return nil, net.ErrClosed
	case <-ctx.Done():
		_, _ = server.Close(), client.Close()

		return nil, ctx.Err()
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// stalledConn returns from each Write stall after its bytes were read.
type stalledConn struct {
	net.Conn
	stall time.Duration
}

func (c stalledConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	time.Sleep(c.stall)

	return n, err
}

// serveOnPipe runs Serve on listener until cancel, then connects a client
// and reads its connection_established. done carries what Serve returns.
func serveOnPipe(t *testing.T, listener *pipeListener) (c *client, cancel context.CancelFunc, done <-chan error) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() {
		served <- Serve(ctx, listener, Config{App: App{ID: testAppID, Key: testKey, Secret: testSecret}}, nil)
	}()

	httpClient := &http.Client{Transport: &http.Transport{DialContext: listener.dial}}
	ws, res, err := websocket.Dial(t.Context(), "ws://realtime.test/app/"+testKey, &websocket.DialOptions{HTTPClient: httpClient})
	closeHandshake(res)
	if err != nil {
		t.Fatal(err)
	}

	c = &client{t: t, ws: ws}
	c.read()

	return c, cancel, served
}

func TestADrainDeliversInFlightAndQueuedFramesBeforeGoingAway(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, cancel, done := serveOnPipe(t, newPipeListener())

		// The client stops reading: the first pong is a write in flight, the
		// second waits in the queue.
		c.send(`{"event":"pusher:ping","data":{}}`)
		c.send(`{"event":"pusher:ping","data":{}}`)
		synctest.Wait()

		// Drain while the first pong is mid-write: the Wait lets Close
		// terminate the connection before the client reads again, so a drain
		// that cut the write fails here.
		cancel()
		synctest.Wait()

		c.expect(`{"event":"pusher:pong"}`)
		c.expect(`{"event":"pusher:pong"}`)
		c.expectClosed(websocket.StatusGoingAway)

		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

// The client has read connection_established, but the server's writer is
// still on its way back from the write when the drain starts: coder/websocket
// keeps that write's context armed until it returns.
func TestADrainWaitsForAWriteTheClientAlreadyRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener := newPipeListener()
		listener.stall = 50 * time.Millisecond
		c, cancel, done := serveOnPipe(t, listener)

		cancel()
		synctest.Wait()

		c.expectClosed(websocket.StatusGoingAway)

		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
