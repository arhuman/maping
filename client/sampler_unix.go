//go:build unix

package maping

import "syscall"

// cpuTimeNs returns cumulative process CPU time (user + system) in nanoseconds
// via getrusage(RUSAGE_SELF). It is a monotonic counter; the sampler diffs it per
// window. On an error it returns 0 so the gauge simply reads zero rather than
// failing the upload.
func cpuTimeNs() uint64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return tvNanos(ru.Utime) + tvNanos(ru.Stime)
}

// tvNanos converts a syscall.Timeval to nanoseconds. Sec/Usec are int64 on Linux
// and int32/int64 on darwin, so both are widened through int64 before scaling.
func tvNanos(tv syscall.Timeval) uint64 {
	return uint64(int64(tv.Sec)*1_000_000_000 + int64(tv.Usec)*1_000)
}
