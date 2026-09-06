package dshproc

import "testing"

// TestURLRegex verifies the URL matcher handles both token and no-token forms.
// Platform-independent (the shell-spawning lifecycle tests live in
// dshproc_unix_test.go, which requires a POSIX shell).
func TestURLRegex(t *testing.T) {
	cases := []struct {
		line string
		url  string
		port string
		ok   bool
	}{
		{"dsh web: http://127.0.0.1:3080/?token=abc123", "http://127.0.0.1:3080/?token=abc123", "3080", true},
		{"dsh web: http://127.0.0.1:3080", "http://127.0.0.1:3080", "3080", true},
		{"dsh web: http://localhost:3199/?token=x-y_z", "http://localhost:3199/?token=x-y_z", "3199", true},
		{"dsh web: http://127.0.0.1:3080 (LAN: ...)", "http://127.0.0.1:3080", "3080", true},
		{"[dsh-clipboard] probe: write=ok", "", "", false},
		{"chrome-devtools-mcp exposes content", "", "", false},
	}
	for _, c := range cases {
		m := URLRe.FindStringSubmatch(c.line)
		if c.ok {
			if m == nil {
				t.Errorf("line %q: expected match, got none", c.line)
				continue
			}
			if m[0] != c.url || m[2] != c.port {
				t.Errorf("line %q: got url=%q port=%q, want url=%q port=%q", c.line, m[0], m[2], c.url, c.port)
			}
		} else if m != nil {
			t.Errorf("line %q: unexpected match %q", c.line, m[0])
		}
	}
}
