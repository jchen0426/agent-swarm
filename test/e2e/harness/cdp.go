//go:build e2e

package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"time"
)

// cdpCallTimeout bounds each call on the pipe, whatever its context's own
// deadline, so that a browser that stops answering fails the call, not the
// test's go test timeout.
const cdpCallTimeout = 20 * time.Second

// errPipeClosed is a call on a pipe that has closed, or that closes before
// its answer comes.
var errPipeClosed = errors.New("harness: the browser's pipe is closed")

// cdpMessage is one message on the pipe (R181): a call, its answer, or an
// event. A call and its answer share an id; an event has none, and names
// the flat session it belongs to, none for the browser's own.
type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
}

// cdpError is an answer that is an error.
type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *cdpError) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("%s (%d): %s", e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("%s (%d)", e.Message, e.Code)
}

// cdpEvent is an event, with the session it came on: "" for the browser's.
type cdpEvent struct {
	Session string
	Method  string
	Params  json.RawMessage
}

// cdp is a client of the DevTools protocol over --remote-debugging-pipe
// (R181): NUL-terminated JSON, written to the browser's file descriptor 3 and
// read from its 4, with flat sessions. It is the standard library's alone.
//
// One reader takes every message off the pipe. An answer goes to the call
// that waits for it, by id; an event goes to its session's queue, which is
// unbounded, so that the reader never waits for a subscriber and a call is
// answered however many events are unread. Each session's subscribers run
// on that session's own goroutine, one event at a time, in the order they
// came; a subscriber may make calls, as the reader keeps reading while it
// runs, but must not wait for a later event of its own session.
type cdp struct {
	r io.ReadCloser
	w io.WriteCloser
	// wmu keeps each message's bytes together on the pipe.
	wmu sync.Mutex

	mu      sync.Mutex
	next    int64
	pending map[int64]chan cdpMessage
	queues  map[string]*cdpQueue
	// err is why the pipe closed; nil while it is open.
	err error
	// done is closed once the pipe has closed.
	done      chan struct{}
	closeOnce sync.Once
	// delivering counts the sessions' goroutines, which end once the pipe
	// has closed and their queues are delivered.
	delivering sync.WaitGroup
}

// newCDP reads r and writes w, the browser's end of each pipe being its 4
// and its 3.
func newCDP(r io.ReadCloser, w io.WriteCloser) *cdp {
	c := &cdp{
		r: r, w: w,
		pending: map[int64]chan cdpMessage{},
		queues:  map[string]*cdpQueue{},
		done:    make(chan struct{}),
	}
	go c.read()
	return c
}

// call sends method with params on session, "" for the browser's, and waits
// for its answer, which it decodes into result unless result is nil. It ends
// with ctx, at cdpCallTimeout at the latest, or when the pipe closes.
func (c *cdp) call(ctx context.Context, session, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(ctx, cdpCallTimeout)
	defer cancel()
	m := cdpMessage{Method: method, SessionID: session}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("harness: CDP %s: %w", method, err)
		}
		m.Params = raw
	}
	answer := make(chan cdpMessage, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return fmt.Errorf("harness: CDP %s: %w", method, err)
	}
	c.next++
	m.ID = c.next
	c.pending[m.ID] = answer
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, m.ID)
		c.mu.Unlock()
	}()
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("harness: CDP %s: %w", method, err)
	}
	if err := c.write(ctx, append(raw, 0)); err != nil {
		return fmt.Errorf("harness: CDP %s: %w", method, err)
	}
	select {
	case a := <-answer:
		return decodeAnswer(method, &a, result)
	case <-c.done:
		// An answer read just before the pipe closed is still the answer.
		select {
		case a := <-answer:
			return decodeAnswer(method, &a, result)
		default:
		}
		return fmt.Errorf("harness: CDP %s: %w", method, c.closedErr())
	case <-ctx.Done():
		return fmt.Errorf("harness: CDP %s: no answer: %w", method, context.Cause(ctx))
	}
}

// decodeAnswer is a call's answer: its error, or its result decoded into
// result unless result is nil.
func decodeAnswer(method string, a *cdpMessage, result any) error {
	if a.Error != nil {
		return fmt.Errorf("harness: CDP %s: %w", method, a.Error)
	}
	if result != nil && len(a.Result) > 0 {
		if err := json.Unmarshal(a.Result, result); err != nil {
			return fmt.Errorf("harness: CDP %s: read its answer: %w", method, err)
		}
	}
	return nil
}

// write puts one message on the pipe, by ctx's deadline when the pipe can
// keep one, as an os.File pipe can; a ctx with none clears an earlier
// call's. A ctx already done writes nothing and returns its error, and so
// does a write that timed out having written nothing: the stream is whole,
// and the client stays open. A write that wrote part of a message has
// broken the stream, as the browser would read the next message's start as
// this one's rest, and closes the client, saying so; any other failed write
// closes it too, saying that the pipe failed.
func (c *cdp) write(ctx context.Context, msg []byte) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if d, ok := c.w.(interface{ SetWriteDeadline(time.Time) error }); ok {
		deadline, _ := ctx.Deadline() // none clears an earlier call's
		_ = d.SetWriteDeadline(deadline)
	}
	n, err := c.w.Write(msg)
	switch {
	case err == nil:
		return nil
	case n == 0 && errors.Is(err, os.ErrDeadlineExceeded):
		// Nothing was written, so the stream is whole: the call failed, not
		// the pipe.
		if cause := context.Cause(ctx); cause != nil {
			return fmt.Errorf("%w: %w", cause, err)
		}
		return fmt.Errorf("%w: %w", context.DeadlineExceeded, err)
	}
	if n > 0 {
		c.shut(fmt.Errorf("%w: a write failed after %d of %d bytes of a message, which breaks the stream: %w", errPipeClosed, n, len(msg), err))
	} else {
		c.shut(fmt.Errorf("%w: a write failed, as the pipe failed: %w", errPipeClosed, err))
	}
	return err
}

// read takes every message off the pipe until it closes.
func (c *cdp) read() {
	br := bufio.NewReaderSize(c.r, 1<<16)
	for {
		raw, err := br.ReadBytes(0)
		if err != nil {
			c.shut(fmt.Errorf("%w: %w", errPipeClosed, err))
			return
		}
		var m cdpMessage
		if err := json.Unmarshal(raw[:len(raw)-1], &m); err != nil {
			c.shut(fmt.Errorf("%w: a message that is not JSON: %w", errPipeClosed, err))
			return
		}
		if m.ID != 0 {
			c.mu.Lock()
			answer := c.pending[m.ID]
			c.mu.Unlock()
			// A call waits for one answer: a second for its id, which no
			// browser sends, is dropped rather than block the reader.
			select {
			case answer <- m:
			default:
			}
			continue
		}
		c.queue(m.SessionID).push(cdpEvent{Session: m.SessionID, Method: m.Method, Params: m.Params})
	}
}

// queue is session's queue, made on first use: a session's events wait in
// it for a subscriber that comes later.
func (c *cdp) queue(session string) *cdpQueue {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.queues[session]
	if q == nil {
		q = newCDPQueue(&c.delivering)
		if c.err != nil {
			q.stop()
		}
		c.queues[session] = q
	}
	return q
}

// subscribe calls h with each of session's events, those already queued
// first, on the session's own goroutine.
func (c *cdp) subscribe(session string, h func(cdpEvent)) {
	c.queue(session).subscribe(h)
}

// forget drops a session that has detached: its subscribers see what its
// queue holds, then no more.
func (c *cdp) forget(session string) {
	c.mu.Lock()
	q := c.queues[session]
	delete(c.queues, session)
	c.mu.Unlock()
	if q != nil {
		q.stop()
	}
}

// shut marks the pipe closed for err, the first reason given, fails every
// call waiting, and stops the queues once they are drained.
func (c *cdp) shut(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	queues := make([]*cdpQueue, 0, len(c.queues))
	for _, q := range c.queues {
		queues = append(queues, q)
	}
	c.mu.Unlock()
	close(c.done)
	for _, q := range queues {
		q.stop()
	}
}

func (c *cdp) closedErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		return errPipeClosed
	}
	return c.err
}

// close closes both ends of the pipe, which a browser takes as the end of
// its session and exits. It is idempotent.
func (c *cdp) close() error {
	var err error
	c.closeOnce.Do(func() {
		c.shut(errPipeClosed)
		err = errors.Join(c.w.Close(), c.r.Close())
	})
	return err
}

// drained waits up to d for every session's goroutine to deliver what its
// queue held and end, as each does once the pipe has closed, and says
// whether all did. A subscriber's call fails at once on a closed pipe, so
// the wait is the subscribers' own work alone.
func (c *cdp) drained(d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		c.delivering.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// cdpQueue is one session's events, unbounded, and its subscribers.
type cdpQueue struct {
	mu   sync.Mutex
	cond *sync.Cond
	// events wait for the session's goroutine; handlers is replaced, never
	// changed in place, so that a goroutine may run a copy of it unlocked.
	events   []cdpEvent
	handlers []func(cdpEvent)
	running  bool
	stopped  bool
	// delivering is the client's count of the sessions' goroutines.
	delivering *sync.WaitGroup
}

func newCDPQueue(delivering *sync.WaitGroup) *cdpQueue {
	q := &cdpQueue{delivering: delivering}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *cdpQueue) push(e cdpEvent) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return
	}
	q.events = append(q.events, e)
	q.cond.Signal()
}

// subscribe adds h, and starts the session's goroutine with the first. A
// subscriber that comes once the pipe has closed starts none, and sees
// nothing.
func (q *cdpQueue) subscribe(h func(cdpEvent)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers = append(slices.Clip(q.handlers), h)
	if !q.running && !q.stopped {
		q.running = true
		q.delivering.Add(1)
		go func() {
			defer q.delivering.Done()
			q.run()
		}()
	}
}

// stop lets the session's goroutine deliver what is queued, then end.
func (q *cdpQueue) stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopped = true
	q.cond.Broadcast()
}

func (q *cdpQueue) run() {
	for {
		q.mu.Lock()
		for len(q.events) == 0 && !q.stopped {
			q.cond.Wait()
		}
		if len(q.events) == 0 {
			q.mu.Unlock()
			return
		}
		e := q.events[0]
		q.events[0] = cdpEvent{}
		q.events = q.events[1:]
		if len(q.events) == 0 {
			q.events = nil
		}
		handlers := q.handlers
		q.mu.Unlock()
		for _, h := range handlers {
			h(e)
		}
	}
}
