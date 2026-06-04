//go:build wasip1

package syscall

//go:wasmimport env get_time
func rusticated_clock() uint64

func clock_time_get(id uint32, precision uint64, time *uint64) Errno {
	*time = rusticated_clock()
	return 0
}
