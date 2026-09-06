// Package updater manages dsh kernel + web-profile plugin updates:
//   - version check via `npm view @deepseek-ai/dsh version`
//   - kernel update via `npm install -g @deepseek-ai/dsh@latest`
//   - web-profile plugin refresh via `pnpm update` in the profile dir
//
// It exposes an async, cancellable workflow with progress events so the UI can
// show state without blocking.
package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// State is the updater's observable state.
type State struct {
	CurrentVersion string    `json:"currentVersion"`
	LatestVersion  string    `json:"latestVersion"`
	UpdateAvail    bool      `json:"updateAvailable"`
	Checking       bool      `json:"checking"`
	Installing     bool      `json:"installing"`
	LastCheck      time.Time `json:"lastCheck"`
	LastError      string    `json:"lastError,omitempty"`
	Log            []string  `json:"log"`
}

// Updater performs update checks and installations.
type Updater struct {
	mu      sync.Mutex
	state   State
	home    string // DSH_HOME (to locate the web profile)
	channel string // "stable" or "alpha" (default alpha)
	onState func(State)
}

// New creates an Updater. home is DSH_HOME; the web profile is expected at
// <home>/profiles/web. channel selects which dist-tag to track ("stable" or
// "alpha").
func New(home string, onState func(State)) *Updater {
	return &Updater{home: home, channel: "alpha", onState: onState}
}

// SetChannel changes the update channel ("stable" or "alpha").
func (u *Updater) SetChannel(ch string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if ch == "stable" {
		u.channel = "stable"
	} else {
		u.channel = "alpha"
	}
}

// StateSnapshot returns a copy of the current state.
func (u *Updater) StateSnapshot() State {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.state
	s.Log = append([]string(nil), u.state.Log...)
	return s
}

func (u *Updater) updateLocked(mut func(*State)) {
	mut(&u.state)
	if u.onState != nil {
		s := u.state
		s.Log = append([]string(nil), u.state.Log...)
		u.onState(s)
	}
}

func (u *Updater) logLine(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.state.Log = append(u.state.Log, s)
	if len(u.state.Log) > 200 {
		u.state.Log = u.state.Log[len(u.state.Log)-200:]
	}
	if u.onState != nil {
		s := u.state
		s.Log = append([]string(nil), u.state.Log...)
		u.onState(s)
	}
}

func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// CurrentVersion returns the installed dsh version via `dsh --version`.
func (u *Updater) CurrentVersion() string {
	out, err := runCmd(context.Background(), "dsh", "--version")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// CheckLatest queries npm for the latest published dsh version. dsh ships on
// prerelease channels (alpha/rc), and `npm view pkg version` only reports the
// `latest` tag, which can lag behind the installed alpha. So we read all
// dist-tags and pick the newest that matches the configured channel:
//   - channel "stable" -> the `latest` tag
//   - channel "alpha"  -> the highest semver across all tags (alpha/rc/stable)
func (u *Updater) CheckLatest(ctx context.Context) (string, error) {
	out, err := runCmd(ctx, "npm", "view", "@deepseek-ai/dsh", "dist-tags", "--json")
	if err != nil {
		return "", err
	}
	var tags map[string]string
	if err := json.Unmarshal([]byte(out), &tags); err != nil {
		// Fall back to plain `version` (the latest tag).
		v, verr := runCmd(ctx, "npm", "view", "@deepseek-ai/dsh", "version")
		if verr != nil {
			return "", verr
		}
		return strings.TrimSpace(v), nil
	}

	if u.channel == "stable" {
		if v, ok := tags["latest"]; ok {
			return v, nil
		}
	}
	// alpha (default): highest semver across all tags.
	best := ""
	for _, v := range tags {
		if best == "" || semverGt(v, best) {
			best = v
		}
	}
	if best == "" {
		return "", fmt.Errorf("no dist-tags found for @deepseek-ai/dsh")
	}
	return best, nil
}

// semverGt reports whether a is a greater version than b (a != b). Handles
// 0.1.2-alpha.4 / 0.1.1-rc.2 style prerelease versions.
func semverGt(a, b string) bool {
	pa, pb := parseVersion(a), parseVersion(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	// Equal core: prerelease-less beats prerelease.
	aPre, bPre := prereleaseOf(a), prereleaseOf(b)
	if aPre == "" && bPre != "" {
		return true
	}
	if aPre != "" && bPre == "" {
		return false
	}
	if aPre == bPre {
		return false
	}
	return aPre > bPre
}

func parseVersion(v string) [3]int {
	var out [3]int
	core := v
	if i := strings.IndexByte(core, '-'); i >= 0 {
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	for i := 0; i < 3 && i < len(parts); i++ {
		fmt.Sscanf(parts[i], "%d", &out[i])
	}
	return out
}

func prereleaseOf(v string) string {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[i+1:]
	}
	return ""
}

// Check fetches the latest version and updates state.
func (u *Updater) Check(ctx context.Context) error {
	u.mu.Lock()
	u.state.Checking = true
	u.state.LastError = ""
	u.updateLocked(func(s *State) { s.Checking = true })
	u.mu.Unlock()

	latest, err := u.CheckLatest(ctx)
	u.mu.Lock()
	u.state.Checking = false
	if err != nil {
		u.state.LastError = "version check failed: " + err.Error()
		u.state.UpdateAvail = false
		u.updateLocked(func(s *State) {
			s.Checking = false
			s.LastError = u.state.LastError
		})
		u.mu.Unlock()
		return err
	}
	u.state.LatestVersion = latest
	u.state.LastCheck = time.Now()
	u.state.CurrentVersion = u.CurrentVersion()
	u.state.UpdateAvail = latest != "" && u.state.CurrentVersion != "" && semverGt(latest, u.state.CurrentVersion)
	u.updateLocked(func(s *State) {
		s.Checking = false
		s.LatestVersion = latest
		s.LastCheck = time.Now()
		s.CurrentVersion = u.state.CurrentVersion
		s.UpdateAvail = u.state.UpdateAvail
		s.LastError = ""
	})
	u.mu.Unlock()
	return nil
}

// resolveInstallVersion picks the exact dsh version to install: the one the
// last channel-aware check selected (state.LatestVersion), or — if no check
// has run yet — computes it now for the current channel.
func (u *Updater) resolveInstallVersion(ctx context.Context) (string, error) {
	u.mu.Lock()
	target := u.state.LatestVersion
	u.mu.Unlock()
	if target != "" {
		return target, nil
	}
	return u.CheckLatest(ctx)
}

// Install updates the dsh kernel and refreshes web-profile plugins.
func (u *Updater) Install(ctx context.Context) error {
	u.mu.Lock()
	if u.state.Installing {
		u.mu.Unlock()
		return fmt.Errorf("install already in progress")
	}
	u.state.Installing = true
	u.updateLocked(func(s *State) { s.Installing = true; s.LastError = "" })
	u.mu.Unlock()

	defer func() {
		u.mu.Lock()
		u.state.Installing = false
		u.updateLocked(func(s *State) { s.Installing = false })
		u.mu.Unlock()
	}()

	// Install the version the channel check selected — NOT `@latest`. The
	// alpha channel tracks the highest published prerelease (e.g.
	// 0.1.2-alpha.5) while the `latest` dist-tag can be an OLDER stable
	// (e.g. 0.1.1-rc.2). Pinning every install to `@latest` silently
	// DOWNGRADED alpha users to the older stable release, whose kernel is
	// incompatible with the alpha web profile (white screen after restart).
	target, err := u.resolveInstallVersion(ctx)
	if err != nil {
		msg := fmt.Sprintf("could not resolve version to install: %v", err)
		u.logLine(msg)
		u.mu.Lock()
		u.state.LastError = msg
		u.updateLocked(func(s *State) { s.LastError = msg })
		u.mu.Unlock()
		return err
	}

	u.logLine("installing dsh kernel: npm install -g @deepseek-ai/dsh@" + target)
	if out, err := runCmd(ctx, "npm", "install", "-g", "@deepseek-ai/dsh@"+target); err != nil {
		msg := fmt.Sprintf("kernel install failed: %v\n%s", err, out)
		u.logLine(msg)
		u.mu.Lock()
		u.state.LastError = msg
		u.updateLocked(func(s *State) { s.LastError = msg })
		u.mu.Unlock()
		return err
	} else {
		u.logLine("kernel install ok: " + out)
	}

	// Refresh web-profile plugins with pnpm.
	profileDir := u.webProfileDir()
	if profileDir != "" {
		u.logLine("refreshing web profile plugins: pnpm update in " + profileDir)
		cmd := exec.CommandContext(ctx, "pnpm", "update")
		cmd.Dir = profileDir
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			msg := fmt.Sprintf("pnpm update failed: %v\n%s", err, out.String())
			u.logLine(msg)
			u.mu.Lock()
			u.state.LastError = msg
			u.updateLocked(func(s *State) { s.LastError = msg })
			u.mu.Unlock()
			// Kernel update succeeded even if profile refresh failed; treat as
			// partial success but surface the error.
		} else {
			u.logLine("pnpm update ok")
		}
	}

	// Refresh local state after install.
	u.mu.Lock()
	u.state.CurrentVersion = u.CurrentVersion()
	u.state.UpdateAvail = false
	u.state.LatestVersion = u.state.CurrentVersion
	u.updateLocked(func(s *State) {
		s.CurrentVersion = u.state.CurrentVersion
		s.UpdateAvail = false
		s.LatestVersion = u.state.CurrentVersion
	})
	u.mu.Unlock()
	return nil
}

func (u *Updater) webProfileDir() string {
	if u.home != "" {
		cand := filepath.Join(u.home, "profiles", "web")
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			return cand
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		cand := filepath.Join(home, ".dsh", "profiles", "web")
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			return cand
		}
	}
	return ""
}
