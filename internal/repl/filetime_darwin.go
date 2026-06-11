//go:build darwin

package repl

import (
	"io/fs"
	"syscall"
	"time"
)

// fileCreated returns the file's birth time on Darwin, falling back to
// ModTime if the syscall info is unavailable.
func fileCreated(info fs.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
	}
	return info.ModTime()
}
