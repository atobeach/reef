package realtime

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// conn is one client connection. Frames reach the socket only through
// send, drained by one writer goroutine; a client that stops reading is
// closed rather than buffered without bound.
type conn struct {
	id string
	ws *websocket.Conn

	send chan []byte
	// done is closed once by terminate; the writer then flushes (if asked)
	// and closes the socket.
	done        chan struct{}
	writerDone  chan struct{}
	closeOnce   sync.Once
	closeCode   websocket.StatusCode
	closeReason string
	flush       bool
	// writeCtx is cancelled by terminate without flush, so a write stuck on
	// a client that stopped reading ends at once. coder/websocket closes the
	// socket without a close frame when a write's context ends, so only a
	// close that gives up on the client cancels it.
	writeCtx    context.Context
	cancelWrite context.CancelFunc

	// lastSeen (unix nanos) and pinged drive the ping/prune cycle.
	lastSeen atomic.Int64
	pinged   atomic.Bool

	// subscriptions are guarded by the server's lock.
	subscriptions map[string]struct{}

	// The rate-limit window, touched only by the reading goroutine.
	windowStart time.Time
	attempts    int
}

func newConn(ctx context.Context, id string, ws *websocket.Conn, buffer int, now time.Time) *conn {
	c := &conn{
		id:            id,
		ws:            ws,
		send:          make(chan []byte, buffer),
		done:          make(chan struct{}),
		writerDone:    make(chan struct{}),
		subscriptions: map[string]struct{}{},
	}
	c.writeCtx, c.cancelWrite = context.WithCancel(ctx)
	c.touch(now)

	return c
}

// touch records activity: any client message counts and answers an
// outstanding ping.
func (c *conn) touch(now time.Time) {
	c.lastSeen.Store(now.UnixNano())
	c.pinged.Store(false)
}

// inactive reports silence for a whole ping interval.
func (c *conn) inactive(now time.Time, interval time.Duration) bool {
	return !now.Before(time.Unix(0, c.lastSeen.Load()).Add(interval))
}

// enqueue queues a frame without blocking. A full queue closes the
// connection with 4100, so pusher-js reconnects after a back-off.
func (c *conn) enqueue(frame []byte) {
	select {
	case <-c.done:
		return
	default:
	}

	select {
	case c.send <- frame:
	default:
		c.terminate(closeOverCapacity, "Over capacity", false)
	}
}

// terminate closes the connection once with code. flush lets the writer
// finish the frame in flight and send what is already queued first (an
// error frame explaining the close, the last frames before a drain);
// without it a frame in flight is cut off and the client gets no close
// frame.
func (c *conn) terminate(code websocket.StatusCode, reason string, flush bool) {
	c.closeOnce.Do(func() {
		c.closeCode = code
		c.closeReason = reason
		c.flush = flush
		close(c.done)

		if !flush {
			c.cancelWrite()
		}
	})
}

// writeLoop is the only goroutine writing to the socket.
func (c *conn) writeLoop(timeout time.Duration) {
	defer close(c.writerDone)
	defer c.cancelWrite()

	for {
		// A pending close wins over queued frames.
		select {
		case <-c.done:
			c.close(timeout)

			return
		default:
		}

		select {
		case frame := <-c.send:
			if err := c.write(frame, timeout); err != nil {
				_ = c.ws.CloseNow()

				return
			}
		case <-c.done:
			c.close(timeout)

			return
		}
	}
}

// close flushes when asked, then does the close handshake, which also
// unblocks the reader.
func (c *conn) close(timeout time.Duration) {
	if c.flush {
		c.drain(timeout)
	}

	_ = c.ws.Close(c.closeCode, c.closeReason)
}

func (c *conn) drain(timeout time.Duration) {
	for {
		select {
		case frame := <-c.send:
			if err := c.write(frame, timeout); err != nil {
				return
			}
		default:
			return
		}
	}
}

func (c *conn) write(frame []byte, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(c.writeCtx, timeout)
	defer cancel()

	return c.ws.Write(ctx, websocket.MessageText, frame)
}

// allow is the per-connection rate limiter: at most MaxAttempts messages
// per Decay window, counted only when allowed.
func (c *conn) allow(now time.Time, limit RateLimit) bool {
	if now.Sub(c.windowStart) >= limit.Decay {
		c.windowStart = now
		c.attempts = 0
	}

	if c.attempts >= limit.MaxAttempts {
		return false
	}

	c.attempts++

	return true
}
