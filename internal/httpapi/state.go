package httpapi

import "sync"

// vaultState holds the process-wide Master Key once the vault has been
// unlocked, by whichever channel got there first: MAILVAULT_MASTER_KEY at
// startup, or a web POST /unlock request. See unlock.go's doc comment for
// why this is deliberately kept separate from a browser's own session —
// the vault being unlocked and a given browser being authenticated are
// related but distinct events.
type vaultState struct {
	mu        sync.RWMutex
	masterKey []byte
}

// unlock records key as the vault's Master Key, if one isn't already set.
// Deriving it twice from the same password/salt is harmless (Argon2id is
// deterministic), but "first successful unlock wins" is simpler to reason
// about than silently overwriting.
func (v *vaultState) unlock(key []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.masterKey == nil {
		v.masterKey = key
	}
}

// key returns the Master Key and true, or nil and false while the vault is
// still locked.
func (v *vaultState) key() ([]byte, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.masterKey, v.masterKey != nil
}
