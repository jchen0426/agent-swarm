package docker

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
)

// standInVersion is the API version a stand-in engine answers the version's
// ping with.
const standInVersion = "1.47"

// standIn serves a stand-in engine on a Unix socket in /tmp, whose path fits
// the 104 bytes macOS allows a socket's: it answers the version's ping, and
// every other request with answer. It returns the engine, Dialed. A
// request's context ends when the client gives the request up, or when the
// test ends, before the server closes, which waits for every handler.
func standIn(t *testing.T, answer http.HandlerFunc) Engine {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "as-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	l, err := net.Listen("unix", filepath.Join(dir, "engine.sock"))
	require.NoError(t, err)
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Header().Set("Api-Version", standInVersion)
			return
		}
		answer(w, r)
	}))
	require.NoError(t, s.Listener.Close())
	s.Listener = l
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	base, stop := context.WithCancel(context.Background())
	s.Config.BaseContext = func(net.Listener) context.Context { return base }
	s.Start()
	t.Cleanup(s.Close)
	t.Cleanup(stop)
	e, err := Dial("unix://" + l.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// answerNothing answers nothing until the request's context ends.
func answerNothing(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }

// breakConnection closes the connection without an answer, as an engine
// that stops mid-call does.
func breakConnection(w http.ResponseWriter, _ *http.Request) {
	if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
		_ = conn.Close()
	}
}

// answerNotFound answers as the engine does for an object it does not have.
func answerNotFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"message":"No such object: x"}`)
}

// answerConflict answers as the engine does for a call it refuses.
func answerConflict(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_, _ = io.WriteString(w, `{"message":"the call is refused"}`)
}

// endsByDeadline returns a context whose deadline ends it at 100 ms, as a
// launch's bound does.
func endsByDeadline(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithTimeout(t.Context(), 100*time.Millisecond)
}

// endsByCancel returns a context that a cancel ends at 100 ms, as a stop's
// does.
func endsByCancel(t *testing.T) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	return ctx, cancel
}

// within runs call with the context ends makes, which ends at 100 ms, and
// returns the call's error; it fails the test when the call does not return
// within 1 s.
func within(t *testing.T, ends func(*testing.T) (context.Context, context.CancelFunc), call func(ctx context.Context) error) error {
	t.Helper()
	ctx, cancel := ends(t)
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- call(ctx) }()
	select {
	case err := <-returned:
		return err
	case <-time.After(time.Second):
		t.Fatal("the call did not return within 1 s, with a context that ended at 100 ms")
		return nil
	}
}

// engineCalls are the port's calls, Close aside, each of the container or
// the image x. gone is what a missing x reads as: nil for a call of no
// object, and for Attach, whose hijack the engine refuses with no reason the
// client reads. conflictIsNil marks Kill, for which the engine's conflict,
// that the container does not run, reads nil (C14, amended).
var engineCalls = []struct {
	name          string
	gone          error
	conflictIsNil bool
	call          func(ctx context.Context, e Engine) error
}{
	{"Version", nil, false, func(ctx context.Context, e Engine) error { _, err := e.Version(ctx); return err }},
	{"Image", ErrNoImage, false, func(ctx context.Context, e Engine) error { _, err := e.Image(ctx, "x"); return err }},
	{"Save", ErrNoImage, false, func(ctx context.Context, e Engine) error { return e.Save(ctx, "x", io.Discard) }},
	{"Load", nil, false, func(ctx context.Context, e Engine) error { return e.Load(ctx, strings.NewReader("a tar")) }},
	{"Tag", ErrNoImage, false, func(ctx context.Context, e Engine) error { return e.Tag(ctx, "x", "agentswarm-it:x") }},
	{"Create", ErrNoImage, false, func(ctx context.Context, e Engine) error {
		_, err := e.Create(ctx, "x", &container.Config{Image: "x"}, &container.HostConfig{})
		return err
	}},
	{"Attach", nil, false, func(ctx context.Context, e Engine) error { _, err := e.Attach(ctx, "x"); return err }},
	{"Start", ErrNoContainer, false, func(ctx context.Context, e Engine) error { return e.Start(ctx, "x") }},
	{"Wait", ErrNoContainer, false, func(ctx context.Context, e Engine) error { return e.Wait(ctx, "x") }},
	{"Inspect", ErrNoContainer, false, func(ctx context.Context, e Engine) error { _, err := e.Inspect(ctx, "x"); return err }},
	{"Kill", ErrNoContainer, true, func(ctx context.Context, e Engine) error { return e.Kill(ctx, "x") }},
	{"Remove", ErrNoContainer, false, func(ctx context.Context, e Engine) error { return e.Remove(ctx, "x") }},
	{"List", nil, false, func(ctx context.Context, e Engine) error { _, err := e.List(ctx, "agentswarm.tenant=x"); return err }},
}

// TestEveryCallReturnsOnceItsContextEnds (Contract C14; Review Focus 2): an
// engine that holds every request does not hold the runner. Each call, its
// context ended at 100 ms by a deadline or by a cancel, returns within 1 s,
// wrapping ErrUnreachable and the context's own error, by which the runner
// tells a stop's cancel from the engine's silence.
func TestEveryCallReturnsOnceItsContextEnds(t *testing.T) {
	e := standIn(t, answerNothing)
	for _, end := range []struct {
		name string
		ends func(*testing.T) (context.Context, context.CancelFunc)
		why  error
	}{
		{"by its deadline", endsByDeadline, context.DeadlineExceeded},
		{"by a cancel", endsByCancel, context.Canceled},
	} {
		for _, c := range engineCalls {
			t.Run(c.name+" "+end.name, func(t *testing.T) {
				err := within(t, end.ends, func(ctx context.Context) error { return c.call(ctx, e) })
				require.ErrorIs(t, err, ErrUnreachable)
				require.ErrorIs(t, err, end.why)
			})
		}
	}
}

// TestAMissingObjectIsToldFromAnEngineThatBreaksOrRefuses (Contract C14):
// the engine's answer that an object is missing reads ErrNoImage or
// ErrNoContainer; a connection the engine breaks reads ErrUnreachable; any
// other refusal reads neither. A kill the engine refuses with a conflict,
// since the container does not run, as when its end races the kill, reads
// nil: a kill wants the container not running, and that holds (C14,
// amended). Any other call's conflict, such as a name in use or a removal in
// progress, stays an error.
func TestAMissingObjectIsToldFromAnEngineThatBreaksOrRefuses(t *testing.T) {
	gone, broken, refused := standIn(t, answerNotFound), standIn(t, breakConnection), standIn(t, answerConflict)
	for _, c := range engineCalls {
		t.Run(c.name, func(t *testing.T) {
			if c.gone != nil {
				err := c.call(t.Context(), gone)
				require.ErrorIs(t, err, c.gone)
				require.NotErrorIs(t, err, ErrUnreachable)
			}

			err := c.call(t.Context(), broken)
			require.ErrorIs(t, err, ErrUnreachable)
			require.NotErrorIs(t, err, context.Canceled)

			err = c.call(t.Context(), refused)
			if c.conflictIsNil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrUnreachable)
			require.NotErrorIs(t, err, ErrNoImage)
			require.NotErrorIs(t, err, ErrNoContainer)
		})
	}
}

// failingWriter fails every write, as a full disk does.
type failingWriter struct{}

var errDiskFull = errors.New("the disk is full")

func (failingWriter) Write([]byte) (int, error) { return 0, errDiskFull }

// TestASaveTellsTheEnginesFailureFromItsWriters: an answer the engine cuts
// short reads ErrUnreachable, and a writer that fails gives its own error,
// not the engine's.
func TestASaveTellsTheEnginesFailureFromItsWriters(t *testing.T) {
	cut := standIn(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/x-tar\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n")
		_ = conn.Close()
	})
	require.ErrorIs(t, cut.Save(t.Context(), "x", io.Discard), ErrUnreachable)

	whole := standIn(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = io.WriteString(w, "a whole tar")
	})
	err := whole.Save(t.Context(), "x", failingWriter{})
	require.ErrorIs(t, err, errDiskFull)
	require.NotErrorIs(t, err, ErrUnreachable)
}

// TestAnAttachTheEngineNeverAnswersEndsWithItsContext (Review Focus 2): the
// moby client's hijack watches no context once it has dialed, so a stand-in
// engine that takes the attach's request and never answers it would hold an
// attach that leaned on the client alone. The stand-in answers the version's
// ping: one that answered nothing would end the call through the ping, which
// honours the context. Once Attach returns, the attach's connection is
// closed, so that nothing waits on it for an answer that never comes.
func TestAnAttachTheEngineNeverAnswersEndsWithItsContext(t *testing.T) {
	var once sync.Once
	held, gone := make(chan struct{}), make(chan struct{})
	e := standIn(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/attach") {
			http.NotFound(w, r)
			return
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		once.Do(func() { close(held) })
		go func() {
			// The read returns once the client closes its connection.
			_, _ = conn.Read(make([]byte, 1))
			close(gone)
		}()
		<-r.Context().Done()
		_ = conn.Close()
	})

	err := within(t, endsByDeadline, func(ctx context.Context) error {
		_, err := e.Attach(ctx, "sandbox")
		return err
	})
	require.ErrorIs(t, err, ErrUnreachable)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-held:
	default:
		t.Fatal("the stand-in never held the attach's request, so the test proves nothing")
	}
	select {
	case <-gone:
	case <-time.After(time.Second):
		t.Fatal("the attach's connection stayed open after Attach returned")
	}
}

// echoAttach answers an attach as the engine does, upgrading the connection;
// then it echoes what the container's input gets on the container's output,
// in the engine's multiplexed frames (stdcopy), until the client closes the
// connection.
func echoAttach(w http.ResponseWriter, _ *http.Request) {
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, err = io.WriteString(conn, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	buf := make([]byte, 4096)
	for err == nil {
		var n int
		n, err = rw.Read(buf)
		if n > 0 {
			frame := make([]byte, 8, 8+n)
			frame[0] = 1 // standard output
			binary.BigEndian.PutUint32(frame[4:], uint32(n))
			if _, werr := conn.Write(append(frame, buf[:n]...)); werr != nil {
				return
			}
		}
	}
}

// TestAnAttachmentOutlivesTheContextItWasAttachedWith is a guard, which
// passes at once: it keeps what a launch relies on. A launch attaches under a
// context that a fence cancels after its fifth step, and that cancel must
// not cut a running turn's input or output (Decision 5). So an attachment
// that Attach returned still writes the container's input, and reads its
// output, after the context it was attached with has ended.
func TestAnAttachmentOutlivesTheContextItWasAttachedWith(t *testing.T) {
	e := standIn(t, echoAttach)
	ctx, cancel := context.WithCancel(t.Context())
	attachment, err := e.Attach(ctx, "sandbox")
	require.NoError(t, err)
	t.Cleanup(func() { _ = attachment.Close() })
	cancel()
	// A cut on the context's end, were there one, would land at once; this
	// gives it ample time to.
	time.Sleep(100 * time.Millisecond)

	_, err = io.WriteString(attachment, "a line\n")
	require.NoError(t, err)
	echo := make([]byte, len("a line\n"))
	read := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(attachment.Output(), echo)
		read <- err
	}()
	select {
	case err := <-read:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("the attachment's output gave back nothing within 1 s")
	}
	require.Equal(t, "a line\n", string(echo))
}

// TestAWaitWhoseAnswerTheEngineHoldsEndsWithItsContext is a guard, which
// passes at once: it keeps what a launch relies on. The engine answers a
// wait with its headers at once, and holds the answer's body until the
// container ends, so a Wait spends its life reading that body, where
// answerNothing's requests wait for the headers. Wait must still end once its
// context does, wrapping ErrUnreachable and the context's error: within 1 s
// of a context of 100 ms, the bound of every call here, as the client ends a
// body's read as soon as the request's context ends.
func TestAWaitWhoseAnswerTheEngineHoldsEndsWithItsContext(t *testing.T) {
	var once sync.Once
	sent := make(chan struct{})
	e := standIn(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/wait") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		once.Do(func() { close(sent) })
		<-r.Context().Done()
	})

	err := within(t, endsByDeadline, func(ctx context.Context) error { return e.Wait(ctx, "sandbox") })
	require.ErrorIs(t, err, ErrUnreachable)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-sent:
	default:
		t.Fatal("the stand-in never sent its answer's headers, so the test proves nothing")
	}
}
