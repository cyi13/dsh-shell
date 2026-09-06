package dshproc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestURLRegex verifies the URL matcher handles both token and no-token forms.
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

// TestStartParsesURL spawns a fake dsh script that prints a URL, then verifies
// the manager parses it and reports status.
func TestStartParsesURL(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fakedsh.sh")
	// A shell script that prints a dsh web line then sleeps.
	content := fmt.Sprintf(`#!/bin/sh
echo "dsh web: http://127.0.0.1:34567/?token=faketok"
sleep 30
`)
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	urlCh := make(chan string, 1)
	stateCh := make(chan Status, 10)
	m := New(script, "", 0, nil,
		func(url string, _ int) { urlCh <- url },
		func(s Status) { stateCh <- s },
		func(string) {})

	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Cleanup()

	select {
	case url := <-urlCh:
		if url != "http://127.0.0.1:34567/?token=faketok" {
			t.Errorf("url = %q", url)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for URL")
	}

	// Status should report running.
	deadline := time.Now().Add(3 * time.Second)
	for {
		s := m.StatusSnapshot()
		if s.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status not running: %+v", s)
		}
		time.Sleep(50 * time.Millisecond)
	}

	m.Stop()
	if m.StatusSnapshot().Running {
		t.Fatal("still running after Stop")
	}
}

// TestAutoRestart verifies that an unexpected exit triggers a relaunch, while a
// deliberate Stop does not.
func TestAutoRestart(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "crashy.sh")
	// Prints a URL then exits non-zero immediately (simulates a crash).
	content := `#!/bin/sh
echo "dsh web: http://127.0.0.1:34568/?token=crash"
exit 1
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	urls := make(chan string, 8)
	m := New(script, "", 0, nil,
		func(url string, _ int) { urls <- url },
		func(Status) {},
		func(string) {})
	m.AutoRestart = true

	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Cleanup()

	// The process exits immediately; the manager should auto-restart it and we
	// should observe at least two URL prints (initial + restart).
	seen := 0
	deadline := time.After(12 * time.Second)
	for seen < 2 {
		select {
		case <-urls:
			seen++
		case <-deadline:
			t.Fatalf("auto-restart did not happen: only %d URL prints", seen)
		}
	}
}

// TestOnUnexpectedExitCallback verifies OnUnexpectedExit fires before an
// automatic restart after a crash (so the shell can show a restarting overlay),
// and that a deliberate Stop does not fire it.
func TestOnUnexpectedExitCallback(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "crashy2.sh")
	// Prints a URL then exits non-zero immediately (simulates a crash).
	content := `#!/bin/sh
echo "dsh web: http://127.0.0.1:34569/?token=crash2"
exit 1
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	events := make(chan string, 16)
	m := New(script, "", 0, nil,
		func(url string, _ int) { events <- "url:" + url },
		func(Status) {},
		func(string) {})
	m.OnUnexpectedExit = func() { events <- "unexpectedExit" }

	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Cleanup()

	// Crash loop: expect at least one unexpectedExit between two URL prints.
	urlsSeen := 0
	exitSeen := false
	deadline := time.After(12 * time.Second)
	for !(urlsSeen >= 2 && exitSeen) {
		select {
		case e := <-events:
			switch {
			case strings.HasPrefix(e, "url:"):
				urlsSeen++
			case e == "unexpectedExit":
				exitSeen = true
			}
		case <-deadline:
			t.Fatalf("timeout: urls=%d exitSeen=%v", urlsSeen, exitSeen)
		}
	}
}

// TestPreflightSuccess verifies Preflight returns nil when a throwaway dsh
// boots and prints its URL.
func TestPreflightSuccess(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "good.sh")
	content := `#!/bin/sh
echo "dsh web: http://127.0.0.1:45600/?token=probe"
sleep 30
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(script, "", 0, nil, nil, nil, nil)
	if err := m.Preflight(10 * time.Second); err != nil {
		t.Fatalf("Preflight should succeed: %v", err)
	}
}

// TestPreflightFailure verifies Preflight returns an error when dsh exits
// without ever becoming ready (simulating a kernel/plugin that cannot boot).
func TestPreflightFailure(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "broken.sh")
	// A script that crashes immediately without printing a URL.
	content := `#!/bin/sh
echo "boom: plugin crashed"
exit 1
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(script, "", 0, nil, nil, nil, nil)
	if err := m.Preflight(10 * time.Second); err == nil {
		t.Fatal("Preflight should fail when dsh cannot boot")
	}
}
