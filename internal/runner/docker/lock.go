package docker

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// lockHolder takes the holder's lock (Contract C12), or fails naming its
// path: <storeDir>/holders/<hex of the tenant>.<hex of the holder>.lock, the
// lowercase hexadecimal of each name's bytes, held with flock(LOCK_EX|LOCK_NB)
// until unlock. The hexadecimal keeps a / in either name from moving the
// path, and keeps the tenant x-y with the holder z apart from x with y-z.
// Instances on one engine share the store, so a holder copied into a second
// deployment is refused here, before the second's start sweep could kill the
// first's turns (Decision 8). The file stays after unlock: one removed while
// another serve opens it would let two serves hold the lock at once.
func lockHolder(storeDir, tenant, holder string) (unlock func(), err error) {
	dir := filepath.Join(storeDir, "holders")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("docker: make the directory of the holders' locks: %w", err)
	}
	path := filepath.Join(dir, hex.EncodeToString([]byte(tenant))+"."+hex.EncodeToString([]byte(holder))+".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // G304: the holder's lock in the store, named in hexadecimal.
	if err != nil {
		return nil, fmt.Errorf("docker: open the holder's lock %s: %w", path, err)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // G115: a file descriptor fits an int.
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("docker: another serve holds the lock %s, of the tenant %q and the holder %q", path, tenant, holder)
		}
		return nil, fmt.Errorf("docker: take the holder's lock %s: %w", path, err)
	}
	// Closing the file releases the lock.
	return sync.OnceFunc(func() { _ = f.Close() }), nil
}
