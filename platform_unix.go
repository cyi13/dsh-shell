//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

// defaultLogPath returns where the persistent log file lives. On macOS the
// conventional spot is ~/Library/Logs (reachable when launched via
// `open`/LaunchServices); other Unix-likes reuse the same shape.
func defaultLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, "Library", "Logs", "dsh-desktop.log")
}

// extraPathDirs lists directories to prepend to PATH on this platform. macOS
// GUI launches inherit no shell PATH, so the common dsh/npm install locations
// are added here.
func extraPathDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "bin"),
		"/usr/local/bin",
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
	}
}
