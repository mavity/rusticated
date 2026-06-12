//go:build wasip1

package runtime

import "internal/runtime/sys"

func netpollinit() {}

func netpollIsPollDescriptor(fd uintptr) bool { return false }

func netpollopen(fd uintptr, pd *pollDesc) int32 { return 0 }

func netpollarm(pd *pollDesc, mode int) {}

func netpolldisarm(pd *pollDesc, mode int32) {}

func removesub(i int) {}

func netpollclose(fd uintptr) int32 { return 0 }

func netpollBreak() {}

func netpoll(delay int64) (gList, int32) {
	if delay != 0 {
		pause(sys.GetCallerSP() - 16)
	}
	return gList{}, 0
}
