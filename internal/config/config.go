// Package config holds the dsh-desktop configuration model and its
// persistence (JSON under ~/Library/Application Support/dsh-desktop).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is the persisted desktop-shell configuration.
type Config struct {
	// Port is the preferred port for the dsh web server (0 = let dsh pick /
	// use default 3080). The shell keeps the port stable across restarts so
	// the same authenticated cookie stays valid for the browser session.
	Port int `json:"port,omitempty"`

	// AutoUpdate enables background update checking/installation.
	AutoUpdate bool `json:"autoUpdate"`

	// UpdateIntervalHours is how often to check for updates.
	UpdateIntervalHours int `json:"updateIntervalHours"`

	// DshBin is an optional explicit path to the dsh binary; empty means
	// resolve from PATH (and the shell's bundled install dir).
	DshBin string `json:"dshBin,omitempty"`

	// HomeDir is the DSH_HOME to launch dsh with; empty means the default
	// (~/.dsh).
	HomeDir string `json:"homeDir,omitempty"`

	// Channel selects the update dist-tag to track: "stable" (the `latest`
	// tag) or "alpha" (the highest published prerelease; default).
	Channel string `json:"channel,omitempty"`

	// ExtraArgs are extra arguments passed to `dsh --profile web` (e.g.
	// --trusted-host). Advanced use only.
	ExtraArgs []string `json:"extraArgs,omitempty"`

	// CloseToTray keeps the app running in the background (dsh keeps serving)
	// when the main window is closed (title-bar close / Cmd+W). When false,
	// closing the window really quits the app.
	CloseToTray bool `json:"closeToTray"`

	// StartMaximized zooms the main window to fill the screen on launch.
	StartMaximized bool `json:"startMaximized"`

	// SyncVscodeURL pushes the dsh web URL (with token when present) into the
	// VS Code dsh-sessions extension's dshSessions.baseUrl setting so the side
	// panel follows the current instance after every restart. Only acts when
	// the extension is installed and the user already has that key. Default
	// off; opt-in.
	SyncVscodeURL bool `json:"syncVscodeURL"`
}

// Default returns the default configuration.
func Default() Config {
	return Config{
		Port:                3080,
		AutoUpdate:          true,
		UpdateIntervalHours: 6,
		Channel:             "alpha",
		CloseToTray:         true,
		StartMaximized:      true,
	}
}

// Path returns the config file path for this platform.
func Path() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "dsh-desktop", "config.json")
}

// Load reads the config from disk, returning defaults when absent.
func Load() (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	// Fill zero-valued fields with defaults so new fields never surprise.
	def := Default()
	if cfg.UpdateIntervalHours <= 0 {
		cfg.UpdateIntervalHours = def.UpdateIntervalHours
	}
	return cfg, nil
}

// Save writes the config atomically (temp file + rename, 0600).
func Save(cfg Config) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
