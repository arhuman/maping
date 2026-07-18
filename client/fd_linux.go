//go:build linux

package maping

import (
	"os"
	"syscall"
)

// fdStats returns the process's open file-descriptor count and the soft
// RLIMIT_NOFILE ceiling, both point-in-time gauges read once per flush window.
// The open count is the number of entries in /proc/self/fd; os.ReadDir opens one
// descriptor for the directory while listing, which the kernel includes in the
// listing, so the count can read +1 — an acceptable ±1 for a saturation gauge.
// On any read error each value falls back to 0 (best-effort, never fails the
// upload).
func fdStats() (open, limit uint64) {
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		open = uint64(len(entries))
	}
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err == nil {
		limit = rl.Cur
	}
	return open, limit
}
