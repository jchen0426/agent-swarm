package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The ID token algorithms accepted are asymmetric alone: no none, and no
// HMAC, which a provider's public key could key (m1).
func TestOnlyAsymmetricSigningAlgorithmsAreAccepted(t *testing.T) {
	assert.NotEmpty(t, signingAlgs)
	for _, alg := range signingAlgs {
		assert.NotEqual(t, "none", strings.ToLower(alg))
		assert.False(t, strings.HasPrefix(alg, "HS"), alg)
	}
}
