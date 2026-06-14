//go:build wasip1

package runtime

func netpollinit() {}

func netpollIsPollDescriptor(fd uintptr) bool { return false }

func netpollopen(fd uintptr, pd *pollDesc) int32 { return 0 }

func netpollarm(pd *pollDesc, mode int) {}

func netpolldisarm(pd *pollDesc, mode int32) {}

func removesub(i int) {}

func netpollclose(fd uintptr) int32 { return 0 }

func netpollBreak() {}

// Dedicated overlapped for the scheduler's netpoll timer.
// It lets the host know when to re-enter the guest so the Go rungime can
// fire its internal timers (context deadlines, time.After, etc.)
var netpollTimerOv overlapped
var netpollTimerActive bool

func netpoll(delay int64) (gList, int32) {
	// Timer regfistration and yielding is handled by beforeIdle and handleAsyncEvent, so we just return here.
	return gList{}, 0
}
