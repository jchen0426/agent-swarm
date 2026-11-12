//go:build e2e

package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBrowser is the far end of a pipe pair: it reads the calls a cdp
// client writes, NUL-terminated JSON, and writes answers and events back, as
// Chrome does on --remote-debugging-pipe (R181).
type fakeBrowser struct {
	t     *testing.T
	out   io.WriteCloser
	wmu   sync.Mutex
	calls chan cdpMessage
}

// newFakeBrowser returns a client wired to a fake browser over in-memory
// pipes, both closed when the test ends.
func newFakeBrowser(t *testing.T) (*cdp, *fakeBrowser) {
	t.Helper()
	toBrowser, fromClient := io.Pipe()
	toClient, fromBrowser := io.Pipe()
	return wireFakeBrowser(t, toBrowser, fromClient, toClient, fromBrowser)
}

// newOSPipeBrowser is newFakeBrowser over os.Pipe, whose files keep write
// deadlines, as Chrome's pipe does.
func newOSPipeBrowser(t *testing.T) (*cdp, *fakeBrowser) {
	t.Helper()
	toBrowser, fromClient, err := os.Pipe()
	require.NoError(t, err)
	toClient, fromBrowser, err := os.Pipe()
	require.NoError(t, err)
	return wireFakeBrowser(t, toBrowser, fromClient, toClient, fromBrowser)
}

func wireFakeBrowser(t *testing.T, toBrowser io.ReadCloser, fromClient io.WriteCloser, toClient io.ReadCloser, fromBrowser io.WriteCloser) (*cdp, *fakeBrowser) {
	t.Helper()
	c := newCDP(toClient, fromClient)
	f := &fakeBrowser{t: t, out: fromBrowser, calls: make(chan cdpMessage, 64)}
	go f.read(bufio.NewReader(toBrowser))
	t.Cleanup(func() {
		_ = c.close()
		_ = fromBrowser.Close()
		_ = toBrowser.Close()
	})
	return c, f
}

func (f *fakeBrowser) read(in *bufio.Reader) {
	defer close(f.calls)
	for {
		raw, err := in.ReadBytes(0)
		if err != nil {
			return
		}
		var m cdpMessage
		if json.Unmarshal(raw[:len(raw)-1], &m) != nil {
			return
		}
		f.calls <- m
	}
}

// next is the next call the client made.
func (f *fakeBrowser) next() cdpMessage {
	f.t.Helper()
	select {
	case m, ok := <-f.calls:
		require.True(f.t, ok, "the client closed its pipe")
		return m
	case <-time.After(5 * time.Second):
		f.t.Fatal("the client made no call")
		return cdpMessage{}
	}
}

// send writes a message to the client. It may run on any goroutine: it
// reports a failure rather than ending the test.
func (f *fakeBrowser) send(m any) {
	raw, err := json.Marshal(m)
	if err == nil {
		f.wmu.Lock()
		_, err = f.out.Write(append(raw, 0))
		f.wmu.Unlock()
	}
	if err != nil {
		f.t.Errorf("the fake browser could not send %v: %v", m, err)
	}
}

// Two calls in flight at once are answered out of order, and each gets its
// own answer; an error answer is the call's error, a call never answered
// ends with its context, and once the pipe closes every call fails.
func TestCDPMatchesAnswersToCalls(t *testing.T) {
	c, f := newFakeBrowser(t)
	type answer struct {
		v   string
		err error
	}
	call := func(method string, out chan<- answer) {
		var r struct {
			V string `json:"v"`
		}
		err := c.call(t.Context(), "", method, map[string]string{"asked": method}, &r)
		out <- answer{r.V, err}
	}
	one, two := make(chan answer, 1), make(chan answer, 1)
	go call("Test.one", one)
	first := f.next()
	go call("Test.two", two)
	second := f.next()
	require.NotEqual(t, first.ID, second.ID)
	assert.JSONEq(t, `{"asked":"Test.one"}`, string(first.Params))
	assert.JSONEq(t, `{"asked":"Test.two"}`, string(second.Params))

	f.send(map[string]any{"id": second.ID, "result": map[string]string{"v": "answer to " + second.Method}})
	f.send(map[string]any{"id": first.ID, "result": map[string]string{"v": "answer to " + first.Method}})
	for _, w := range []struct {
		ch   chan answer
		want string
	}{{two, "answer to Test.two"}, {one, "answer to Test.one"}} {
		select {
		case a := <-w.ch:
			require.NoError(t, a.err)
			assert.Equal(t, w.want, a.v)
		case <-time.After(5 * time.Second):
			t.Fatalf("no answer for %q", w.want)
		}
	}

	failed := make(chan error, 1)
	go func() { failed <- c.call(t.Context(), "s1", "Test.fails", nil, nil) }()
	m := f.next()
	assert.Equal(t, "s1", m.SessionID, "a session's call carries its session")
	f.send(map[string]any{"id": m.ID, "sessionId": "s1", "error": map[string]any{"code": -32000, "message": "No such thing"}})
	var ce *cdpError
	require.ErrorAs(t, <-failed, &ce)
	assert.Equal(t, -32000, ce.Code)
	assert.Equal(t, "No such thing", ce.Message)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, c.call(ctx, "", "Test.never", nil, nil), context.DeadlineExceeded)
	_ = f.next()

	// A call still waiting when the pipe closes ends at once, with
	// errPipeClosed, and so does any call after.
	pending := make(chan error, 1)
	go func() { pending <- c.call(t.Context(), "", "Test.pending", nil, nil) }()
	assert.Equal(t, "Test.pending", f.next().Method)
	require.NoError(t, f.out.Close())
	select {
	case err := <-pending:
		require.ErrorIs(t, err, errPipeClosed)
	case <-time.After(time.Second):
		t.Fatal("a call pending when the pipe closed still waits")
	}
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the client did not see its pipe close")
	}
	require.ErrorIs(t, c.call(t.Context(), "", "Test.after", nil, nil), errPipeClosed, "a call on a closed pipe")
}

// The reader never waits for a subscriber: 10,000 events no one has read
// leave a call answered at once, and a subscriber may make a call from
// inside its own handler.
func TestCDPReaderNeverBlocks(t *testing.T) {
	c, f := newFakeBrowser(t)
	release := make(chan struct{})
	defer close(release)
	var taken atomic.Int64
	c.subscribe("s1", func(cdpEvent) {
		taken.Add(1)
		<-release
	})
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for i := range 10000 {
			f.send(map[string]any{"method": "Test.event", "sessionId": "s1", "params": map[string]int{"i": i}})
		}
		for i := range 1000 {
			f.send(map[string]any{"method": "Test.unwatched", "sessionId": "s9", "params": map[string]int{"i": i}})
		}
	}()
	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatal("the reader stopped reading while the events were unread")
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	answered := make(chan error, 1)
	var pong struct {
		OK bool `json:"ok"`
	}
	go func() { answered <- c.call(ctx, "", "Test.ping", nil, &pong) }()
	m := f.next()
	f.send(map[string]any{"id": m.ID, "result": map[string]bool{"ok": true}})
	require.NoError(t, <-answered, "the call is answered within a second")
	assert.True(t, pong.OK)
	assert.Equal(t, int64(1), taken.Load(), "the subscriber holds its first event; the rest wait in its queue")

	inside := make(chan string, 1)
	c.subscribe("s2", func(e cdpEvent) {
		var r struct {
			Echo string `json:"echo"`
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.call(ctx, e.Session, "Test.inside", nil, &r); err != nil {
			inside <- "error: " + err.Error()
			return
		}
		inside <- r.Echo
	})
	f.send(map[string]any{"method": "Test.trigger", "sessionId": "s2"})
	m = f.next()
	assert.Equal(t, "Test.inside", m.Method)
	assert.Equal(t, "s2", m.SessionID)
	f.send(map[string]any{"id": m.ID, "sessionId": "s2", "result": map[string]string{"echo": "from inside"}})
	select {
	case got := <-inside:
		assert.Equal(t, "from inside", got)
	case <-time.After(5 * time.Second):
		t.Fatal("the call made inside a handler was never answered")
	}
}

// Each session's subscribers see that session's events alone, in order, and
// the browser's own events go to the browser's subscribers; a session's
// events wait for a subscriber that comes later.
func TestCDPRoutesEventsBySession(t *testing.T) {
	c, f := newFakeBrowser(t)
	var mu sync.Mutex
	got := map[string][]string{}
	subscribe := func(session string) {
		c.subscribe(session, func(e cdpEvent) {
			mu.Lock()
			defer mu.Unlock()
			got[session] = append(got[session], e.Session+"/"+e.Method)
		})
	}
	subscribe("")
	subscribe("A")
	subscribe("B")
	for _, e := range []map[string]any{
		{"method": "X.one", "sessionId": "A"},
		{"method": "X.two", "sessionId": "B"},
		{"method": "X.three"},
		{"method": "X.four", "sessionId": "A"},
		{"method": "X.five", "sessionId": "C"},
		{"method": "X.six", "sessionId": "B"},
	} {
		f.send(e)
	}
	count := func(session string) int {
		mu.Lock()
		defer mu.Unlock()
		return len(got[session])
	}
	require.Eventually(t, func() bool { return count("A") == 2 && count("B") == 2 && count("") == 1 }, 5*time.Second, 10*time.Millisecond)
	subscribe("C")
	require.Eventually(t, func() bool { return count("C") == 1 }, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"A/X.one", "A/X.four"}, got["A"])
	assert.Equal(t, []string{"B/X.two", "B/X.six"}, got["B"])
	assert.Equal(t, []string{"/X.three"}, got[""])
	assert.Equal(t, []string{"C/X.five"}, got["C"], "an event that came before its session's subscriber waited for it")
}

// A write that wrote nothing leaves the stream whole, and the client open: a
// call whose context is already done writes nothing and returns its
// context's error, and the next call on the same client is answered; and a
// write with no deadline, after one with a deadline that has passed, is not
// failed by the old deadline. It runs over os.Pipe, whose files keep write
// deadlines, as Chrome's pipe does.
func TestCDPAWriteThatWroteNothingLeavesThePipeOpen(t *testing.T) {
	c, f := newOSPipeBrowser(t)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, c.call(cancelled, "", "Test.cancelled", nil, nil), context.Canceled)
	expired, cancelExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancelExpired()
	require.ErrorIs(t, c.call(expired, "", "Test.expired", nil, nil), context.DeadlineExceeded)

	answered := make(chan error, 1)
	go func() { answered <- c.call(t.Context(), "", "Test.after", nil, nil) }()
	m := f.next()
	assert.Equal(t, "Test.after", m.Method, "the calls whose context was done wrote nothing")
	f.send(map[string]any{"id": m.ID, "result": map[string]any{}})
	require.NoError(t, <-answered, "the client is still open")

	soon, cancelSoon := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelSoon()
	require.NoError(t, c.write(soon, []byte(`{"id":-1,"method":"Test.soon"}`+"\x00")))
	<-soon.Done()
	require.NoError(t, c.write(context.Background(), []byte(`{"id":-2,"method":"Test.later"}`+"\x00")),
		"a write with no deadline is not held to an earlier one's")
	assert.Equal(t, "Test.soon", f.next().Method)
	assert.Equal(t, "Test.later", f.next().Method)
}

// A write that wrote part of its message and then ran out of time has
// broken the stream, as Chrome would read the next message's start as this
// one's rest: it closes the client, saying so. A message larger than the
// pipe's buffer, with nobody reading, writes part and times out.
func TestCDPAPartialWriteClosesThePipe(t *testing.T) {
	unread, fromClient, err := os.Pipe()
	require.NoError(t, err)
	toClient, silent, err := os.Pipe()
	require.NoError(t, err)
	c := newCDP(toClient, fromClient)
	t.Cleanup(func() {
		_ = c.close()
		_ = unread.Close()
		_ = silent.Close()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	require.Error(t, c.call(ctx, "", "Test.big", map[string]string{"blob": strings.Repeat("x", 4<<20)}, nil))
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("a write that broke the stream left the client open")
	}
	assert.Contains(t, c.closedErr().Error(), "of a message", "the reason says the message was cut")
	require.ErrorIs(t, c.call(t.Context(), "", "Test.after", nil, nil), errPipeClosed)
}

// lateCtx is a context whose deadline has passed while its own timer has not
// yet run to cancel it, as a context.WithTimeout is between its deadline and
// its timer's goroutine: the window the first walk's Eval fell into, past
// every check of Err, so that only the write's own error can tell.
type lateCtx struct{ context.Context }

func (lateCtx) Deadline() (time.Time, bool) { return time.Now().Add(-time.Millisecond), true }

// A write at its deadline, its context not yet done, sets a deadline already
// past and writes nothing: the call fails with the deadline's error, and the
// client stays open, its next call answered. This is the branch that keeps
// the first walk's fault from coming back.
func TestCDPAWriteAtItsDeadlineLeavesThePipeOpen(t *testing.T) {
	c, f := newOSPipeBrowser(t)
	base, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	late := lateCtx{base}
	require.NoError(t, late.Err(), "not yet done: the checks of Err pass")
	err := c.call(late, "", "Test.late", nil, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-c.done:
		t.Fatalf("a write that wrote nothing closed the client: %v", c.closedErr())
	default:
	}
	answered := make(chan error, 1)
	go func() { answered <- c.call(t.Context(), "", "Test.after", nil, nil) }()
	m := f.next()
	assert.Equal(t, "Test.after", m.Method, "the late call wrote nothing")
	f.send(map[string]any{"id": m.ID, "result": map[string]any{}})
	require.NoError(t, <-answered)
}

// A write that fails for any reason but its deadline, though it wrote
// nothing, closes the client, saying that the pipe failed, not that a
// message was cut: the browser's end is gone.
func TestCDPABrokenPipeWriteClosesTheClient(t *testing.T) {
	gone, fromClient, err := os.Pipe()
	require.NoError(t, err)
	toClient, silent, err := os.Pipe()
	require.NoError(t, err)
	c := newCDP(toClient, fromClient)
	t.Cleanup(func() {
		_ = c.close()
		_ = silent.Close()
	})
	require.NoError(t, gone.Close())
	require.Error(t, c.call(t.Context(), "", "Test.nobody", nil, nil))
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("a write to a pipe with no reader left the client open")
	}
	why := c.closedErr().Error()
	assert.Contains(t, why, "the pipe failed", "a write that wrote nothing broke no message")
	assert.NotContains(t, why, "breaks the stream")
	require.ErrorIs(t, c.call(t.Context(), "", "Test.after", nil, nil), errPipeClosed)
}
