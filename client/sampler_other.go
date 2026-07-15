//go:build !unix

package maping

// cpuTimeNs returns 0 on platforms without a portable process-CPU-time syscall
// wired up. The CPU gauge is best-effort, so an unsupported platform simply
// reports zero rather than blocking the rest of the InstanceWindow.
func cpuTimeNs() uint64 { return 0 }
