// Package proc finds the claude process a hook runs under and tells later whether it still runs: from the /proc
// file system on Linux, from ps on macOS.
package proc

// Process is a pid together with its start time, so that a pid the kernel reused for another process is not
// taken for the original one.
type Process struct {
	PID   int
	Start uint64
}
