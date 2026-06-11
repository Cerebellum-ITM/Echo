//go:build !darwin

package repl

import (
	"io/fs"
	"time"
)

// fileCreated approximates creation time with ModTime on platforms where
// Go does not expose the birth time.
func fileCreated(info fs.FileInfo) time.Time {
	return info.ModTime()
}
