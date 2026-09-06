package main

import (
	"fmt"
	"net"
)

// isPortFree reports whether a TCP port is available on loopback.
func isPortFree(port int) bool {
	if port <= 0 {
		return true
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// resolvePort picks the port the shell should use: the configured preferred
// port if free, otherwise the first free port starting from the preferred one.
// Returns the chosen port and whether it differs from the configured one.
func resolvePort(preferred int) (chosen int, changed bool) {
	if preferred <= 0 {
		preferred = 3080
	}
	if isPortFree(preferred) {
		return preferred, false
	}
	// Scan a small range for a free port.
	for p := preferred + 1; p < preferred+100; p++ {
		if isPortFree(p) {
			return p, true
		}
	}
	// Last resort: let the OS pick.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return preferred, false
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, true
}
