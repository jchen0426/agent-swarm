package auth_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
)

// A return path is accepted only if it is a path on this site, to a page
// that is not sign-in's own: no other host, however spelled, no backslash,
// control character or percent-encoded slash or backslash, and no /auth/*
// target (design D4; n7).
func TestSafeReturnRefusesOpenRedirects(t *testing.T) {
	for _, p := range []string{"/" + strings.Repeat("a", 2047), "/items%20x", "/items%2e", "/items%7e", "/items%20%3F", "/", "/items", "/items?incident=inc-1&x=%20y", "/items/inc-1#act", "/kill-switch", "/authors", "/a/b/../c"} {
		got, ok := auth.SafeReturn(p)
		assert.True(t, ok, p)
		assert.Equal(t, p, got, p)
	}
	for _, p := range []string{
		"", "items", "//evil", "//evil.example.com/items", "/\\evil", "\\\\evil", "/items\\..\\..\\evil",
		"%2F%2Fevil", "/%2F%2Fevil", "/%2fevil", "/%5Cevil", "/%5cevil", "https://evil", "http://evil/items",
		"javascript:alert(1)", "https:/evil", "/items\r\nSet-Cookie: x=y", "/items\x00", "/items\tx", "/items\x7f",
		"/itéms", "/items//evil", "/auth/signin", "/auth/callback?code=x", "/auth/signout", "/auth/fresh",
		"/auth", "/auth/", "/x/../auth/callback", "/./auth/signin", "/%61uth/callback", "/auth%2Fcallback",
		"/items%0d%0aSet-Cookie:%20x=y", "/%00", "/items%1F", "/items%1f", "/items%7f", "/items%7F", "/items?x=%0A",
		"/" + strings.Repeat("a", 2048),
	} {
		_, ok := auth.SafeReturn(p)
		assert.False(t, ok, "%q", p)
	}
}
