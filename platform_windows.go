//go:build windows

package main

import (
	"os"
	"path/filepath"
)

// defaultLogPath returns where the persistent log file lives. On Windows that
// is %LOCALAPPDATA%\dsh-desktop\logs\dsh-desktop.log (LOCALAPPDATA normally
// resolves to %USERPROFILE%\AppData\Local).
func defaultLogPath() string {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		root = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(root, "dsh-desktop", "logs", "dsh-desktop.log")
}

// extraPathDirs lists directories to prepend to PATH on this platform. Windows
// GUI launches inherit the user PATH, so nothing is strictly needed; we still
// add the npm global bin (where `npm i -g` puts dsh.cmd) when present.
func extraPathDirs() []string {
	if p := os.Getenv("APPDATA"); p != "" {
		return []string{filepath.Join(p, "npm")}
	}
	return nil
}
