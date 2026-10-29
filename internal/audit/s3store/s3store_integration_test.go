//go:build integration

package s3store_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
	"github.com/jchen0426/agent-swarm/internal/audit/s3store"
)

// image is an S3 store with object lock, pinned by digest.
const image = "quay.io/minio/minio:RELEASE.2024-12-18T13-15-44Z@sha256:1dce27c494a16bae114774f1cec295493f3613142713130c2d22dd5696be6ad3"

const user, password = "anchor-owner", "anchor-owner-password"

var endpoint string

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, image,
		testcontainers.WithExposedPorts("9000/tcp"),
		testcontainers.WithEnv(map[string]string{"MINIO_ROOT_USER": user, "MINIO_ROOT_PASSWORD": password}),
		testcontainers.WithCmd("server", "/data"),
		// make test-clean finds the container by the first label, and saves the
		// files at the path of the second before it removes it.
		testcontainers.WithLabels(map[string]string{
			"agentswarm.project": "agent-swarm",
			"agentswarm.data":    "/data",
		}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/minio/health/cluster").WithPort("9000")),
	)
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			slog.ErrorContext(ctx, "remove the S3 container", "error", err)
		}
	}()
	if err != nil {
		slog.ErrorContext(ctx, "start the S3 container", "error", err)
		return 1
	}
	published, err := ctr.PortEndpoint(ctx, "9000/tcp", "")
	if err != nil {
		slog.ErrorContext(ctx, "read the S3 endpoint", "error", err)
		return 1
	}
	// The store takes plain HTTP for a loopback host only, and testcontainers
	// running in a container reaches a published port at the bridge's
	// gateway, so the tests reach the store through a loopback listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		slog.ErrorContext(ctx, "listen on the loopback", "error", err)
		return 1
	}
	defer func() { _ = ln.Close() }()
	go forward(ln, published)
	endpoint = ln.Addr().String()
	return m.Run()
}

// forward copies each connection ln accepts, both ways, to a connection of
// its own to addr, until either end closes.
func forward(ln net.Listener, addr string) {
	for {
		in, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = in.Close() }()
			out, err := net.Dial("tcp", addr)
			if err != nil {
				return
			}
			defer func() { _ = out.Close() }()
			go func() {
				_, _ = io.Copy(out, in)
				_ = out.Close()
			}()
			_, _ = io.Copy(in, out)
		}()
	}
}

// list lists the tenant's objects, and reads each that is no delete marker,
// with its retention when retained is set.
func list(t *testing.T, s *s3store.Store, tenant string, retained bool) []chain.Stored {
	t.Helper()
	got, err := s.List(t.Context(), tenant)
	require.NoError(t, err)
	for i := range got {
		if got[i].Deleted {
			continue
		}
		require.NoError(t, s.Read(t.Context(), &got[i]))
		if retained {
			require.NoError(t, s.Retention(t.Context(), &got[i]))
		}
	}
	return got
}

// owner is the bucket owner's client.
func owner(t *testing.T) *minio.Client {
	t.Helper()
	c, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(user, password, "")})
	require.NoError(t, err)
	return c
}

// bucket makes a bucket as its owner would: with object lock, and a default
// retention of one day in mode, unless mode is empty.
func bucket(t *testing.T, name string, lock bool, mode minio.RetentionMode) *s3store.Store {
	t.Helper()
	c := owner(t)
	require.NoError(t, c.MakeBucket(t.Context(), name, minio.MakeBucketOptions{ObjectLocking: lock}))
	if mode != "" {
		days, unit := uint(1), minio.Days
		require.NoError(t, c.SetObjectLockConfig(t.Context(), name, &mode, &days, &unit))
	}
	s, err := s3store.New(&s3store.Config{Endpoint: endpoint, Insecure: true, Bucket: name, Prefix: "anchors/"}, user, password)
	require.NoError(t, err)
	return s
}

// The store takes plain HTTP for a loopback host only, so the tests reach
// their store at one, even when they run in a container, where testcontainers
// reaches a published port at the bridge's gateway.
func TestTheStoreTakesTheTestsEndpointOverPlainHTTP(t *testing.T) {
	_, err := s3store.New(&s3store.Config{Endpoint: endpoint, Insecure: true, Bucket: "any"}, user, password)

	require.NoError(t, err)
}

// An anchor is written once: a second write under its name is refused, so
// the platform never makes a second version of an anchor.
func TestAnAnchorIsWrittenOnce(t *testing.T) {
	s := bucket(t, "written-once", true, minio.Compliance)

	require.NoError(t, s.Put(t.Context(), "tenant-a/a.json", []byte("one")))
	err := s.Put(t.Context(), "tenant-a/a.json", []byte("two"))

	require.ErrorIs(t, err, app.ErrExists)
	got := list(t, s, "tenant-a", true)
	require.Len(t, got, 1)
	assert.Equal(t, "tenant-a/a.json", got[0].Key)
	assert.Equal(t, []byte("one"), got[0].Body)
	assert.WithinDuration(t, time.Now().AddDate(0, 0, 1), got[0].LockedUntil, time.Hour, "the bucket's default retention holds it a day")
}

// A version without retention in compliance mode, as in a bucket whose
// default retention is governance, is listed as not locked.
func TestAVersionWithoutComplianceRetentionIsNotLocked(t *testing.T) {
	s := bucket(t, "governance", true, minio.Governance)
	require.NoError(t, s.Put(t.Context(), "tenant-a/a.json", []byte("one")))

	got := list(t, s, "tenant-a", true)

	require.Len(t, got, 1)
	assert.True(t, got[0].LockedUntil.IsZero())
}

// A listing shows every version and every delete marker: what someone with
// the platform's right to put objects can add never hides an anchor.
func TestTheListingShowsSecondVersionsAndDeleteMarkers(t *testing.T) {
	s := bucket(t, "versions", true, minio.Compliance)
	ctx := t.Context()
	require.NoError(t, s.Put(ctx, "tenant-a/a.json", []byte("one")))
	c := owner(t)
	_, err := c.PutObject(ctx, "versions", "anchors/tenant-a/a.json", bytes.NewReader([]byte("two")), 3, minio.PutObjectOptions{})
	require.NoError(t, err)
	require.NoError(t, c.RemoveObject(ctx, "versions", "anchors/tenant-a/a.json", minio.RemoveObjectOptions{}))
	require.NoError(t, s.Put(ctx, "tenant-b/b.json", []byte("other tenant")))

	got := list(t, s, "tenant-a", true)

	var bodies []string
	var markers int
	for _, o := range got {
		assert.Equal(t, "tenant-a/a.json", o.Key)
		if o.Deleted {
			markers++
			continue
		}
		bodies = append(bodies, string(o.Body))
	}
	assert.ElementsMatch(t, []string{"one", "two"}, bodies)
	assert.Equal(t, 1, markers)
}

// Only a bucket with object lock and a default retention in compliance mode
// keeps anchors write-once.
func TestOnlyComplianceRetentionIsWriteOnce(t *testing.T) {
	for name, want := range map[string]struct {
		lock bool
		mode minio.RetentionMode
		ok   bool
	}{
		"compliance": {true, minio.Compliance, true},
		"governance": {true, minio.Governance, false},
		"no-default": {true, "", false},
		"no-lock":    {false, "", false},
	} {
		s := bucket(t, "lock-"+name, want.lock, want.mode)

		locked, why, err := s.Locked(t.Context())

		require.NoError(t, err, name)
		assert.Equal(t, want.ok, locked, name)
		if !want.ok {
			assert.NotEmpty(t, why, name)
		}
	}
}

// A bucket without object lock is listed and read all the same:
// verification reports the store unlocked, and anchoring goes on.
func TestABucketWithoutObjectLockIsListed(t *testing.T) {
	s := bucket(t, "unlocked", false, "")
	require.NoError(t, s.Put(t.Context(), "tenant-a/a.json", []byte("one")))

	got := list(t, s, "tenant-a", false)

	require.Len(t, got, 1)
	assert.Equal(t, []byte("one"), got[0].Body)
	assert.True(t, got[0].LockedUntil.IsZero())
}
