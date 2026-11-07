//go:build unix

package app

import (
	"os/signal"
	"syscall"
)

// ignoreBrokenPipe makes a write to a closed standard output or standard error fail with EPIPE instead
// of ending the process with SIGPIPE, which Go does for file descriptors 1 and 2 by default. The server
// writes its announcements there and must not stop because the reader went away.
func ignoreBrokenPipe() { signal.Ignore(syscall.SIGPIPE) }
