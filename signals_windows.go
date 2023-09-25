//go:build windows
// +build windows

package main

func setupSignals(notifyReload func()) {
	// NOOP: Windows doesn't have signals equivalent to the Unix world.
}
