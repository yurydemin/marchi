package httpapi

import "testing"

func TestUnlockLockout_LocksAfterThreshold(t *testing.T) {
	l := newUnlockLockout()
	ip := "203.0.113.1"

	for i := 0; i < unlockLockoutThreshold-1; i++ {
		l.recordFailure(ip)
		if _, locked := l.lockedUntil(ip); locked {
			t.Fatalf("locked out after only %d failures, want threshold %d", i+1, unlockLockoutThreshold)
		}
	}

	l.recordFailure(ip)
	if _, locked := l.lockedUntil(ip); !locked {
		t.Fatalf("not locked out after %d failures, want locked", unlockLockoutThreshold)
	}
}

func TestUnlockLockout_SuccessClearsHistory(t *testing.T) {
	l := newUnlockLockout()
	ip := "203.0.113.2"

	for i := 0; i < unlockLockoutThreshold-1; i++ {
		l.recordFailure(ip)
	}
	l.recordSuccess(ip)

	// One more failure right after a success shouldn't be treated as the
	// threshold-th of the earlier run — recordSuccess must have reset the
	// count to zero, not just paused it.
	l.recordFailure(ip)
	if _, locked := l.lockedUntil(ip); locked {
		t.Fatal("locked out after 1 failure following a recorded success, want the earlier failures cleared")
	}
}

func TestUnlockLockout_IsolatedPerIP(t *testing.T) {
	l := newUnlockLockout()
	attacker, innocent := "203.0.113.3", "203.0.113.4"

	for i := 0; i < unlockLockoutThreshold; i++ {
		l.recordFailure(attacker)
	}
	if _, locked := l.lockedUntil(attacker); !locked {
		t.Fatal("attacker IP should be locked out")
	}
	if _, locked := l.lockedUntil(innocent); locked {
		t.Fatal("a different IP must not be locked out by another IP's failures")
	}
}
