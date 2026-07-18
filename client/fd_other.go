//go:build !linux

package maping

// fdStats returns 0, 0 on platforms where the open file-descriptor gauges are not
// wired up. Counting open FDs and reading RLIMIT_NOFILE needs an OS-specific read
// (currently only Linux /proc/self/fd + Getrlimit), so on darwin/dev and other
// platforms both read 0 — expected and best-effort, the gauges simply report zero
// rather than blocking the rest of the InstanceWindow.
func fdStats() (open, limit uint64) { return 0, 0 }
