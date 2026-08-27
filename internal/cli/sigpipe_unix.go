//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package cli

import (
	"os/signal"
	"syscall"
)

// ignoreBrokenPipeSignal makes a closed JSONL stdout report EPIPE to the
// publisher instead of terminating r42 before research can finish.
func ignoreBrokenPipeSignal() {
	signal.Ignore(syscall.SIGPIPE)
}
