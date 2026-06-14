//go:build wasip1

package runtime

import "internal/runtime/sys"

// pause is declared in runtime/stubs_wasm.go - do not redeclare here.
// It sets PAUSE=1 and executes RETUNWIND to yield control back to the host.

const (
	mutex_unlocked = 0
	mutex_locked   = 1

	active_spin     = 4
	active_spin_cnt = 30
)

type mWaitList struct{}

func lockVerifyMSize()             {}
func mutexContended(l *mutex) bool { return false }

func lock(l *mutex) {
	lock2(l)
}

func lock2(l *mutex) {
	if l.key == mutex_locked {
		// throw("self deadlock")
	}
	gp := getg()
	gp.m.locks++
	l.key = mutex_locked
}

func unlock(l *mutex) {
	unlock2(l)
}

func unlock2(l *mutex) {
	if l.key == mutex_unlocked {
		// throw("unlock of unlocked lock")
	}
	gp := getg()
	gp.m.locks--
	l.key = mutex_unlocked
}

func noteclear(n *note) { n.key = 0 }

func notewakeup(n *note) {
	n.key = 1
}

func notesleep(n *note) {
	for n.key == 0 {
		pause(sys.GetCallerSP() - 16)
	}
}

func notetsleep(n *note, ns int64) bool {
	deadline := nanotime() + ns
	for n.key == 0 {
		if ns >= 0 && nanotime() >= deadline {
			return false
		}
		pause(sys.GetCallerSP() - 16)
	}
	return true
}

func notetsleepg(n *note, ns int64) bool {
	gp := getg()
	if gp == gp.m.g0 {
		throw("notetsleepg on g0")
	}

	deadline := nanotime() + ns
	for {
		if n.key != 0 {
			return true
		}
		if ns >= 0 && nanotime() >= deadline {
			return false
		}
		pause(sys.GetCallerSP() - 16)
	}
}

//go:yeswritebarrierrec
func beforeIdle(now, pollUntil int64) (gp *g, otherReady bool) {
	if pollUntil != 0 {
		delay := pollUntil - now - 1
		if delay < 1 {
			delay = 1
		}
		if delay > 1e9 {
			delay = 1e9
		}

		setNetpollTimer(uint32(delay))
	}

	go handleAsyncEvent()
	return nil, true
}

func checkTimeouts() {}
