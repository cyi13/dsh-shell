package main

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// VS Code + dsh-sessions integration.
//
// The dsh-sessions VS Code extension shows DSH in the side panel; when DSH
// authentication is enabled it needs the full URL (including ?token=…) in the
// dshSessions.baseUrl user setting, and it listens for that setting changing
// and hot-reloads. So, when the shell starts dsh and learns the real URL, we
// can push it into the VS Code user settings.json so the side panel follows
// the current token automatically — no manual paste after every restart.
//
// This is deliberately conservative:
//   - only acts when the user turned the feature on (config.SyncVscodeURL);
//   - only acts when the dsh-sessions extension is actually installed
//     (otherwise updating the setting is pointless);
//   - only edits the value of an EXISTING "dshSessions.baseUrl" key — it never
//     creates that key for a user who did not already opt into the extension;
//   - writes atomically (temp file + rename) and keeps the rest of the file
//     byte-for-byte identical, including JSONC comments.

// vscodeSettingsDirCandidates returns, per platform, the VS Code user settings
// directories to probe (the stable path first). macOS uses
// ~/Library/Application Support/Code/User.
func vscodeSettingsDirCandidates(home string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "Code", "User")}
	case "windows":
		return []string{filepath.Join(home, "AppData", "Roaming", "Code", "User")}
	default:
		return []string{filepath.Join(home, ".config", "Code", "User")}
	}
}

// vscodeExtensionsDirCandidates returns the directories that may contain the
// dsh-sessions extension. On macOS the global extensions live under
// ~/.vscode/extensions; other Code-family products (Cursor etc.) use their own
// directories but the user only asked about VS Code itself.
func vscodeExtensionsDirCandidates(home string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{filepath.Join(home, ".vscode", "extensions")}
	case "windows":
		return []string{filepath.Join(home, ".vscode", "extensions")}
	default:
		return []string{filepath.Join(home, ".vscode", "extensions")}
	}
}

// dshSessionsExtensionInstalled reports whether the dsh-sessions extension is
// present in any candidate extensions directory. The publisher is "cyi13" and
// the extension id prefix is "cyi13.dsh-sessions".
func dshSessionsExtensionInstalled(home string) bool {
	for _, dir := range vscodeExtensionsDirCandidates(home) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "cyi13.dsh-sessions-") {
				return true
			}
		}
	}
	return false
}

// replaceBaseURLSetting rewrites the value of an existing
// "dshSessions.baseUrl" key inside a JSONC settings file. It only operates on
// the exact property line `"dshSessions.baseUrl": "…"`, replacing the quoted
// value, and returns the new content. If the key does not exist, or the value
// is already the new one, it returns ok=false so the caller can skip writing.
func replaceBaseURLSetting(content, newValue string) (string, bool) {
	re := regexp.MustCompile(`(?m)^(\s*"dshSessions\.baseUrl"\s*:\s*)"([^"]*)"(\s*,?)$`)
	loc := re.FindStringSubmatchIndex(content)
	if loc == nil {
		return content, false
	}
	oldValue := content[loc[4]:loc[5]]
	if oldValue == newValue {
		return content, false
	}
	// loc: full match then group1 (prefix), group2 (old value), group3 (suffix)
	replacement := content[loc[2]:loc[3]] + "\"" + newValue + "\"" + content[loc[6]:loc[7]]
	out := content[:loc[0]] + replacement + content[loc[1]:]
	return out, true
}

// syncVscodeURL pushes the current dsh URL into the VS Code dsh-sessions
// baseUrl setting, subject to the guards described above. It returns true when
// it actually wrote the setting. It never fails the caller: problems are
// logged and skipped.
func syncVscodeURL(home, currentURL string) bool {
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		} else {
			return false
		}
	}
	if currentURL == "" {
		return false
	}
	// Guard 1: is the dsh-sessions extension installed? Otherwise writing the
	// setting is pointless.
	if !dshSessionsExtensionInstalled(home) {
		log.Printf("[vscode] dsh-sessions extension not installed, skip URL sync")
		return false
	}

	settingsPath := ""
	for _, dir := range vscodeSettingsDirCandidates(home) {
		p := filepath.Join(dir, "settings.json")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			settingsPath = p
			break
		}
	}
	if settingsPath == "" {
		log.Printf("[vscode] no VS Code user settings.json found, skip URL sync")
		return false
	}

	content, err := os.ReadFile(settingsPath)
	if err != nil {
		log.Printf("[vscode] read %s: %v", settingsPath, err)
		return false
	}

	// Guard 2: only touch an existing dshSessions.baseUrl key, and only when
	// its value actually differs (replaceBaseURLSetting reports ok=false for
	// an absent key or an unchanged value — the latter avoids needless VS Code
	// reloads on every restart).
	newContent, found := replaceBaseURLSetting(string(content), currentURL)
	if !found {
		log.Printf("[vscode] dshSessions.baseUrl not present (or unchanged) in %s, skip URL sync", settingsPath)
		return false
	}

	// Atomic write (temp + rename) so a half-written file never lands.
	tmp := settingsPath + ".dsh-tmp"
	if err := os.WriteFile(tmp, []byte(newContent), 0o600); err != nil {
		log.Printf("[vscode] write temp settings: %v", err)
		return false
	}
	if err := os.Rename(tmp, settingsPath); err != nil {
		log.Printf("[vscode] replace settings: %v", err)
		_ = os.Remove(tmp)
		return false
	}
	log.Printf("[vscode] updated dshSessions.baseUrl -> %s in %s", currentURL, settingsPath)
	return true
}
