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

func lockVerifyMSize() {}
func mutexContended(l *mutex) bool { return false }

func lock(l *mutex) {
	lockWithRank(l, getLockRank(l))
}

func lock2(l *mutex) {
	if l.key == mutex_locked {
		throw("self deadlock")
	}
	gp := getg()
	if gp.m.locks < 0 {
		throw("lock count")
	}
	gp.m.locks++
	l.key = mutex_locked
}

func unlock(l *mutex) {
	unlockWithRank(l)
}

func unlock2(l *mutex) {
	if l.key == mutex_unlocked {
		throw("unlock of unlocked lock")
	}
	gp := getg()
	gp.m.locks--
	if gp.m.locks < 0 {
		throw("lock count")
	}
	l.key = mutex_unlocked
}

func noteclear(n *note) { n.key = 0 }

func notewakeup(n *note) {
	if n.key != 0 {
		throw("notewakeup - double wakeup")
	}
	n.key = 1
}

func notesleep(n *note) {
	throw("notesleep not supported by wasi")
}

func notetsleep(n *note, ns int64) bool {
	throw("notetsleep not supported by wasi")
	return false
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
		pause(sys.GetCallerSP() - 16)
		if ns >= 0 && nanotime() >= deadline {
			return false
		}
	}
}

func beforeIdle(int64, int64) (*g, bool) {
	pause(sys.GetCallerSP() - 16)
	return nil, false
}

func checkTimeouts() {}
