//go:build linux

package maping

import (
	"os"
	"strconv"
	"strings"
)

// rssBytes returns true resident set size in bytes from /proc/self/statm, whose
// second whitespace-separated field is the resident page count. It is a
// point-in-time gauge read once per flush window. On any read or parse error it
// returns 0 so the gauge simply reads zero rather than failing the upload.
func rssBytes() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}
