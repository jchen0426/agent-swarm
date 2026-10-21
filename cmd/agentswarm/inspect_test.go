package main

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A database that takes inspect's connection and never answers makes inspect
// fail within its bound, saying so, rather than hang: the listener never
// accepts, so the system takes the connection, and nothing answers it.
func TestInspectGivesUpOnADatabaseThatDoesNotAnswer(t *testing.T) {
	const password = "inspect-secret-pass"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	deployment, _ := servedDeployment(t, "postgres://inspect:"+password+"@"+ln.Addr().String()+"/agentswarm?sslmode=disable", nil)

	done := make(chan error, 1)
	go func() { done <- inspect(t.Context(), deployment, inspectItem, "an-item", io.Discard, time.Second) }()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the database did not answer within 1s")
		assert.NotContains(t, err.Error(), password)
	case <-time.After(15 * time.Second):
		t.Fatal("inspect hangs on a database that does not answer")
	}
}
