package s3store_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/s3store"
)

// Plain HTTP reaches a loopback host only, where tests run their store; an
// anchor store anywhere else is reached over HTTPS.
func TestPlainHTTPIsForALoopbackHostOnly(t *testing.T) {
	for _, host := range []string{"127.0.0.1:9000", "localhost:9000", "[::1]:9000"} {
		_, err := s3store.New(&s3store.Config{Endpoint: host, Insecure: true, Bucket: "b"}, "id", "secret")
		require.NoError(t, err, host)
	}
	for _, host := range []string{"anchors.example:9000", "10.0.0.1:9000", "localhost.example:9000"} {
		_, err := s3store.New(&s3store.Config{Endpoint: host, Insecure: true, Bucket: "b"}, "id", "secret")
		require.ErrorContains(t, err, "loopback", host)
	}
	_, err := s3store.New(&s3store.Config{Endpoint: "anchors.example", Bucket: "b"}, "id", "secret")
	require.NoError(t, err)
	_, err = s3store.New(&s3store.Config{Endpoint: "anchors.example", Bucket: "b"}, "", "secret")
	require.Error(t, err)
}
