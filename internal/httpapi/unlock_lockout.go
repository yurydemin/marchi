package httpapi

import (
	"sync"
	"time"
)

// unlockLockoutThreshold/Duration implement a progressive lockout after
// repeated failed unlock attempts — distinct from unlockRateLimit's
// blanket "100 req/min" ceiling (unlock.go), which caps request *volume*
// but never actually stops a determined, patient password-guesser: at
// 100/min it still lets through 144,000 guesses/day forever. This tracks
// failures specifically (a wrong password, not every request) per
// source IP, and once threshold is hit within one lockout window, blocks
// that IP from even attempting again until the window passes — turning
// "slow but unlimited" into "bounded".
const (
	unlockLockoutThreshold = 10
	unlockLockoutDuration  = 15 * time.Minute
)

// unlockLockout is intentionally in-memory only, like vaultState and the
// session store — a restart clears it, same as it clears every other
// unlock-adjacent piece of runtime state, and that's an acceptable
// tradeoff for a self-hosted single-process app rather than a public
// multi-instance service that would need a shared store to make this
// meaningful across replicas.
type unlockLockout struct {
	mu   sync.Mutex
	byIP map[string]*lockoutEntry
}

type lockoutEntry struct {
	failures    int
	lockedUntil time.Time
}

func newUnlockLockout() *unlockLockout {
	return &unlockLockout{byIP: make(map[string]*lockoutEntry)}
}

// lockedUntil reports whether ip is currently locked out and, if so,
// until when. An entry whose lock has already expired is treated as not
// locked (its failure count was already reset when the lock was set, so
// there's nothing stale to clear here — the next recordFailure starts a
// fresh count).
func (l *unlockLockout) lockedUntil(ip string) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.byIP[ip]
	if !ok || !time.Now().Before(e.lockedUntil) {
		return time.Time{}, false
	}
	return e.lockedUntil, true
}

// recordFailure counts one more wrong-password attempt from ip, locking
// it out for unlockLockoutDuration once unlockLockoutThreshold is
// reached. The counter resets to 0 at that point rather than continuing
// to climb, so a lockout always needs a fresh run of threshold failures
// to trigger again after it expires — not one more on top of whatever
// was left over.
func (l *unlockLockout) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.byIP[ip]
	if !ok {
		e = &lockoutEntry{}
		l.byIP[ip] = e
	}
	e.failures++
	if e.failures >= unlockLockoutThreshold {
		e.lockedUntil = time.Now().Add(unlockLockoutDuration)
		e.failures = 0
	}
}

// recordSuccess clears ip's failure history entirely — a correct
// password is a clean slate, not just a pause in counting.
func (l *unlockLockout) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
}
