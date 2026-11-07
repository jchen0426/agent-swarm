package app_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// An item's id, and an opening's digest, are what the admin API derived
// before they moved to app (Plan 9a-1, R85): the ids of a fixed table of
// keys, and the digests of a fixed table of openings, as the admin API gave
// them. An opening's title is left out of its digest, and routing and
// responders left out read as empty.
func TestItemIDAndOpeningDigestAreStableAcrossTheMove(t *testing.T) {
	for _, tc := range []struct {
		tenant, key string
		id          item.ID
	}{
		{"example", "k", "570bafd1-059a-8223-bd23-7c15cf039092"},
		{"example", "web:abc", "eba3d1c9-ac98-84d7-b1b5-3ddaf27bc655"},
		{"acme", "2026-11-08/checkout", "78bf978e-c122-800a-8e6c-dde4f93b637a"},
		{"", "", "6e340b9c-ffb3-8a98-9ca5-44e6bb780a2c"},
		{"t", "ключ", "1c4fd190-fd8d-8d54-8166-83a74022acc9"},
	} {
		assert.Equal(t, tc.id, app.ItemID(tc.tenant, tc.key), "%q %q", tc.tenant, tc.key)
	}

	const bare = "sha256:ec2807449051e9f9c97c2cf96b2a0a7cea63d32d16752cf2dd1e8518828b27fb"
	for name, tc := range map[string]struct {
		in   app.OpenInput
		want string
	}{
		"a bare incident": {in: app.OpenInput{Type: "incident"}, want: bare},
		"a full opening, its title, source, id and actor left out": {
			in: app.OpenInput{
				ID: "x", Type: "incident", Title: "t", Source: "person", Routing: map[string]string{"service": "checkout", "region": "eu"},
				Principal: "alice@example.com", Responders: []string{"bob@example.com"}, Actor: app.Actor{Subject: "admin", Admin: true},
			},
			want: "sha256:7f85120164bac0c6cb9bf9457d1aa6d19aff6184f8dde1acda0332f3835fab7e",
		},
		"empty routing and responders, as if left out": {
			in: app.OpenInput{Type: "incident", Routing: map[string]string{}, Responders: []string{}}, want: bare,
		},
		"another title": {in: app.OpenInput{Type: "incident", Title: "another"}, want: bare},
	} {
		assert.Equal(t, tc.want, app.OpeningDigest(&tc.in), name)
	}
}
