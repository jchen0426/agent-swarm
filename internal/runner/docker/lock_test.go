package docker

import (
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheHoldersLockIsNamedByItsTenantAndHolderInHexadecimal (Decision 8):
// the tenant x-y with the holder z, and x with y-z, lock two files, and a /
// in either name keeps the file in holders/.
func TestTheHoldersLockIsNamedByItsTenantAndHolderInHexadecimal(t *testing.T) {
	dir := t.TempDir()
	for _, pair := range [][2]string{{"x-y", "z"}, {"x", "y-z"}, {"../a/b", "c/../.."}} {
		unlock, err := lockHolder(dir, pair[0], pair[1])
		require.NoError(t, err, "the tenant %q and the holder %q", pair[0], pair[1])
		t.Cleanup(unlock)
	}
	slashes := hex.EncodeToString([]byte("../a/b")) + "." + hex.EncodeToString([]byte("c/../..")) + ".lock"
	assert.ElementsMatch(t, []string{"782d79.7a.lock", "78.792d7a.lock", slashes}, dirNames(t, filepath.Join(dir, "holders")))
	assert.Equal(t, []string{"holders"}, dirNames(t, dir), "no lock lies outside holders/")
}

// TestASecondLockOfOneTenantAndHolderIsRefused (Review Focus 4): while one
// serve holds a tenant's and holder's lock, another's lockHolder fails,
// naming the lock's path; once the first unlocks, it succeeds.
func TestASecondLockOfOneTenantAndHolderIsRefused(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockHolder(dir, "acme", "a")
	require.NoError(t, err)

	_, err = lockHolder(dir, "acme", "a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), filepath.Join(dir, "holders", "61636d65.61.lock"))

	other, err := lockHolder(dir, "acme", "b")
	require.NoError(t, err, "another holder of the tenant has a lock of its own")
	other()

	unlock()
	again, err := lockHolder(dir, "acme", "a")
	require.NoError(t, err)
	again()
}
