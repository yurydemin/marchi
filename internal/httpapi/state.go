package httpapi

import "sync"

// vaultState holds the process-wide Data Encryption Key (DEK — see
// internal/security/masterkey's package doc for why every subkey derives
// from this, not the password-derived Master Key directly) and the
// backend built from it, once the vault has been unlocked by whichever
// channel got there first: MARCHI_MASTER_KEY at startup, or a web POST
// /unlock request. See unlock.go's doc comment for why this is
// deliberately kept separate from a browser's own session — the vault
// being unlocked and a given browser being authenticated are related but
// distinct events.
type vaultState struct {
	build func([]byte) (*backend, error)

	mu      sync.Mutex
	dek     []byte
	backend *backend
}

func newVaultState(build func([]byte) (*backend, error)) *vaultState {
	return &vaultState{build: build}
}

// unlock records dek as the vault's Data Encryption Key and builds the
// backend, if one isn't already set — re-deriving it twice from the same
// password/salt is harmless (Argon2id is deterministic and the DEK
// unwrap is idempotent), but "first successful unlock wins" is simpler to
// reason about than silently rebuilding. Returns the (possibly
// pre-existing) backend.
func (v *vaultState) unlock(dek []byte) (*backend, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.dek != nil {
		return v.backend, nil
	}
	b, err := v.build(dek)
	if err != nil {
		return nil, err
	}
	v.dek = dek
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
