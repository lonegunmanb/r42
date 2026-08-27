//go:build windows || plan9 || js || wasip1

package cli

func ignoreBrokenPipeSignal() {}
