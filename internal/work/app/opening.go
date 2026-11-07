package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// ItemID derives an item's id from its opening's key (Plan 7d-1, R15): a
// UUID of version 8 whose bits are the first 128 of SHA-256 over the tenant,
// a zero byte, and the key, with the version and variant bits set. It has
// the form of the ids the platform makes, which are of version 7, and cannot
// be one of them. Each front end keeps its keys apart from the other's (Plan
// 9a-1, R85): the web's carry the prefix "web:".
func ItemID(tenant, key string) item.ID {
	sum := sha256.Sum256([]byte(tenant + "\x00" + key))
	var u uuid.UUID
	copy(u[:], sum[:16])
	u[6] = u[6]&0x0f | 0x80 // version 8
	u[8] = u[8]&0x3f | 0x80 // the variant of RFC 9562, RFC 4122's
	return item.ID(u.String())
}

// OpeningDigest is an opening's digest, which its item's opening records
// (Decision 14): SHA-256 over the JSON of its type, routing, principal and
// responders, every object's keys sorted, routing and responders left out
// read as empty. The title is left out: redaction may change it rather than
// refuse it, and a digest over it would keep, unsalted, a hash of a secret
// the redaction removed. A retry that changes only the title gets the item.
// The id, the source and the actor are left out too: they are not the
// request's.
func OpeningDigest(in *OpenInput) string {
	routing, responders := in.Routing, in.Responders
	if routing == nil {
		routing = map[string]string{}
	}
	if responders == nil {
		responders = []string{}
	}
	// Strings alone, which always encode.
	canonical, _ := json.Marshal(map[string]any{"type": in.Type, "routing": routing, "principal": in.Principal, "responders": responders})
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}
