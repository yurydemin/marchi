package httpapi

import "sync"

// vaultState holds the process-wide Master Key and the backend built from
// it, once the vault has been unlocked by whichever channel got there
// first: MAILVAULT_MASTER_KEY at startup, or a web POST /unlock request.
// See unlock.go's doc comment for why this is deliberately kept separate
// from a browser's own session — the vault being unlocked and a given
// browser being authenticated are related but distinct events.
type vaultState struct {
	build func([]byte) (*backend, error)

	mu        sync.Mutex
	masterKey []byte
	backend   *backend
}

func newVaultState(build func([]byte) (*backend, error)) *vaultState {
	return &vaultState{build: build}
}

// unlock records key as the vault's Master Key and builds the backend, if
// one isn't already set — deriving the key twice from the same
// password/salt is harmless (Argon2id is deterministic), but "first
// successful unlock wins" is simpler to reason about than silently
// rebuilding. Returns the (possibly pre-existing) backend.
func (v *vaultState) unlock(key []byte) (*backend, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.masterKey != nil {
		return v.backend, nil
	}
	b, err := v.build(key)
	if err != nil {
		return nil, err
	}
	v.masterKey = key
	v.backend = b
	return b, nil
}

// currentBackend returns the backend if the vault has been unlocked, or
// nil otherwise — used at shutdown, where there's nothing left to build.
func (v *vaultState) currentBackend() *backend {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.backend
}
