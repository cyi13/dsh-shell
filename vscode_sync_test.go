package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReplaceBaseURLSetting covers the two real JSONC layouts: the key with a
// trailing comma (mid-file) and without (last entry before the closing brace),
// plus comments elsewhere that must be preserved byte-for-byte.
func TestReplaceBaseURLSetting(t *testing.T) {
	// Last-entry form (matches the user's actual settings.json: key is the
	// final property before "}" with no trailing comma).
	lastEntry := `{
	"workbench.colorTheme": "Dark+",
	"dshSessions.zoomScale": 0.9,
	"dshSessions.baseUrl": "http://127.0.0.1:3080"
}`
	got, ok := replaceBaseURLSetting(lastEntry, "http://127.0.0.1:3080/?token=abc")
	if !ok {
		t.Fatalf("expected key found")
	}
	want := `{
	"workbench.colorTheme": "Dark+",
	"dshSessions.zoomScale": 0.9,
	"dshSessions.baseUrl": "http://127.0.0.1:3080/?token=abc"
}`
	if got != want {
		t.Errorf("last-entry replace mismatch:\n got: %q\nwant: %q", got, want)
	}
	// closing brace must survive
	if !strings.HasSuffix(strings.TrimSpace(got), "}") {
		t.Errorf("closing brace lost:\n%s", got)
	}

	// Mid-file form (trailing comma) + a comment above.
	midFile := `{
	// some comment that must survive
	"dshSessions.baseUrl": "http://127.0.0.1:3080",
	"editor.fontWeight": "300"
}`
	got2, ok2 := replaceBaseURLSetting(midFile, "http://127.0.0.1:3080/?token=xyz")
	if !ok2 {
		t.Fatalf("expected key found (mid-file)")
	}
	want2 := `{
	// some comment that must survive
	"dshSessions.baseUrl": "http://127.0.0.1:3080/?token=xyz",
	"editor.fontWeight": "300"
}`
	if got2 != want2 {
		t.Errorf("mid-file replace mismatch:\n got: %q\nwant: %q", got2, want2)
	}

	// Value unchanged -> reported as unchanged.
	_, ok3 := replaceBaseURLSetting(lastEntry, "http://127.0.0.1:3080")
	if ok3 {
		t.Fatalf("same value should report no change")
	}

	// Key absent -> ok=false, content untouched.
	noKey := `{ "a": 1 }`
	out4, ok4 := replaceBaseURLSetting(noKey, "x")
	if ok4 {
		t.Fatalf("absent key should report not-found")
	}
	if out4 != noKey {
		t.Errorf("absent-key content changed")
	}
}

// TestDshSessionsExtensionInstalled exercises the detector against a fake
// extensions dir in both present and absent cases.
func TestDshSessionsExtensionInstalled(t *testing.T) {
	home := t.TempDir()
	ext := filepath.Join(home, ".vscode", "extensions")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if dshSessionsExtensionInstalled(home) {
		t.Fatal("should be false with empty extensions dir")
	}
	// Create a dsh-sessions extension folder.
	inst := filepath.Join(ext, "cyi13.dsh-sessions-0.1.0")
	if err := os.MkdirAll(inst, 0o755); err != nil {
		t.Fatal(err)
	}
	if !dshSessionsExtensionInstalled(home) {
		t.Fatal("should detect installed dsh-sessions extension")
	}
}

// TestSyncVscodeURL guards: no extension -> no write; no settings file -> no
// write; no baseUrl key -> no write; with everything present -> writes and
// preserves the rest of the file.
func TestSyncVscodeURL(t *testing.T) {
	home := t.TempDir()
	ext := filepath.Join(home, ".vscode", "extensions")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ext, "cyi13.dsh-sessions-0.1.0"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Case: no settings.json -> no write.
	if syncVscodeURL(home, "http://127.0.0.1:3080/?token=a") {
		t.Fatal("should not write without a settings.json")
	}

	userDir := filepath.Join(home, "Library", "Application Support", "Code", "User")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(userDir, "settings.json")
	original := "{\n\t\"editor.fontWeight\": \"300\",\n\t\"dshSessions.baseUrl\": \"http://127.0.0.1:3080\"\n}"
	if err := os.WriteFile(settingsPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// Case: no baseUrl key -> no write.
	noKey := "{\n\t\"editor.fontWeight\": \"300\"\n}"
	if err := os.WriteFile(settingsPath, []byte(noKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if syncVscodeURL(home, "http://127.0.0.1:3080/?token=b") {
		t.Fatal("should not write when dshSessions.baseUrl key is absent")
	}
	if err := os.WriteFile(settingsPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// Case: everything present -> writes new URL, preserves the rest.
	if !syncVscodeURL(home, "http://127.0.0.1:3080/?token=newtok") {
		t.Fatal("expected a write")
	}
	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"dshSessions.baseUrl": "http://127.0.0.1:3080/?token=newtok"`) {
		t.Fatalf("new value not written:\n%s", got)
	}
	if !strings.Contains(string(got), `"editor.fontWeight": "300"`) {
		t.Fatalf("unrelated content changed:\n%s", got)
	}
	// No temp file left behind.
	if _, err := os.Stat(settingsPath + ".dsh-tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file not cleaned up")
	}
}
