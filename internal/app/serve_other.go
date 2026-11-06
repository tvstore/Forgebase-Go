//go:build !unix

package app

// ignoreBrokenPipe: there is no SIGPIPE on this platform.
func ignoreBrokenPipe() {}
