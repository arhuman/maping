//go:build !linux

package maping

// rssBytes returns 0 on platforms where true resident set size is not wired up.
// True RSS needs an OS-specific read (currently only Linux /proc/self/statm), so
// on darwin/dev and other platforms it reads 0 — expected and best-effort, the
// gauge simply reports zero rather than blocking the rest of the InstanceWindow.
func rssBytes() uint64 { return 0 }
