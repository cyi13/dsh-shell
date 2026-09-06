// Package dshproc manages the `dsh --profile web` child process: spawning it,
// watching stdout for the printed URL, reporting lifecycle, and restarting on
// crash. It is the desktop shell's bridge to the dsh web server.
package dshproc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

// Status describes the current dsh child lifecycle.
type Status struct {
	Running  bool   `json:"running"`
	Pid      int    `json:"pid"`
	URL      string `json:"url"` // full authenticated URL (may be bare when local-no-auth)
	Port     int    `json:"port"`
	Home     string `json:"home"`
	Bin      string `json:"bin"`
	ExitCode int    `json:"exitCode"`
	Err      string `json:"err,omitempty"`
}

// URLRe matches the line `dsh web: http://127.0.0.1:PORT` with optional
// `?token=...` (token absent when local-no-auth is configured).
var URLRe = regexp.MustCompile(`https?://(127\.0\.0\.1|localhost|\[::1\]):(\d+)(?:/\?token=\S+)?`)

// LogFunc receives child stdout/stderr lines (and other diagnostics).
type LogFunc func(line string)

// Manager owns the dsh child process.
type Manager struct {
	mu       sync.Mutex
	bin      string
	home     string
	port     int
	extra    []string
	cmd      *exec.Cmd
	pty      *os.File // pty master (when running under a pseudo-terminal)
	url      string
	running  bool
	exit     int
	errMsg   string
	stopping bool // true when Stop/Cleanup was requested by the user/app

	// AutoRestart re-launches dsh if it exits unexpectedly (crash/kill).
	AutoRestart bool

	// OnUnexpectedExit, when set, is invoked (on a fresh goroutine, outside the
	// manager lock) right before an automatic restart of a dsh that exited on
	// its own (crash/kill). It lets the shell drop a "restarting…" overlay into
	// the live dsh page so the user never stares at a dead connection.
	OnUnexpectedExit func()

	onURL   func(url string, port int) // called once the URL is parsed
	onState func(status Status)        // called on any lifecycle change
	log     LogFunc

	// context cancelled to stop the stdout scanner
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a Manager. bin/home/port/extra come from config; onURL is
// invoked when the dsh URL is available; onState on lifecycle changes.
func New(bin, home string, port int, extra []string, onURL func(string, int), onState func(Status), log LogFunc) *Manager {
	return &Manager{
		bin:         bin,
		home:        home,
		AutoRestart: true,
		port:        port,
		extra:       extra,
		onURL:       onURL,
		onState:     onState,
		log:         log,
		done:        make(chan struct{}),
	}
}

// StatusSnapshot returns the current status without locking side effects.
func (m *Manager) StatusSnapshot() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Manager) snapshotLocked() Status {
	s := Status{
		Running:  m.running,
		Pid:      0,
		URL:      m.url,
		Port:     m.port,
		Home:     m.home,
		Bin:      m.bin,
		ExitCode: m.exit,
		Err:      m.errMsg,
	}
	if m.cmd != nil && m.cmd.Process != nil {
		s.Pid = m.cmd.Process.Pid
	}
	return s
}

// notifyLocked captures a snapshot and fires the state callback WITHOUT
// holding m.mu, so the callback may re-enter the manager (e.g. StatusSnapshot
// from a tray menu rebuild) without deadlocking.
func (m *Manager) notifyLocked() {
	if m.onState != nil {
		// Snapshot is already valid under the caller's lock; copy the
		// callback ref so we can drop the lock before invoking.
		snap := m.snapshotLocked()
		cb := m.onState
		// The caller holds m.mu; we must not invoke while holding it. This
		// function is only ever called while the lock is held, so schedule the
		// callback after unlock via a fresh goroutine.
		go cb(snap)
	}
}

func (m *Manager) logf(format string, args ...any) {
	if m.log != nil {
		m.log(fmt.Sprintf(format, args...))
	}
}

// replaceEnv returns env with the PWD entry replaced by value (or appended if
// absent), preserving other entries.
func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := env[:0]
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			out = append(out, prefix+value)
			found = true
		} else {
			out = append(out, e)
		}
	}
	if !found {
		out = append(out, prefix+value)
	}
	return out
}

// resolveBin returns the dsh executable path.
func (m *Manager) resolveBin() (string, error) {
	if m.bin != "" {
		if _, err := os.Stat(m.bin); err == nil {
			return m.bin, nil
		}
		return "", fmt.Errorf("configured dsh binary not found: %s", m.bin)
	}
	bin, err := exec.LookPath("dsh")
	if err != nil {
		return "", fmt.Errorf("dsh not found on PATH: %w", err)
	}
	return bin, nil
}

// Start launches dsh. It is safe to call again to restart.
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil
	}

	bin, err := m.resolveBin()
	if err != nil {
		m.errMsg = err.Error()
		m.notifyLocked()
		return err
	}
	m.bin = bin

	args := []string{"--profile", "web", "--no-open"}
	if m.port > 0 {
		args = append(args, "--port", fmt.Sprintf("%d", m.port))
	}
	args = append(args, m.extra...)

	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	if m.home != "" {
		cmd.Env = append(cmd.Env, "DSH_HOME="+m.home)
	}
	// GUI-launched processes can inherit a corrupted PWD (non-ASCII paths get
	// mangled by LaunchServices), which dsh may read as its workspace root and
	// then hang opening files. Give dsh a clean, real working directory and a
	// consistent PWD so it never sees the mangled one.
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
		cmd.Env = replaceEnv(cmd.Env, "PWD", home)
	}

	// dsh hangs during startup when it has no controlling terminal (as
	// happens when spawned from a GUI-launched app: no TTY, stdin=/dev/null).
	// Run it under a pseudo-terminal so it always has one. The pty master is
	// what we scan for the printed URL.
	master, err := pty.Start(cmd)
	if err != nil {
		m.errMsg = err.Error()
		m.notifyLocked()
		return fmt.Errorf("start dsh (pty): %w", err)
	}
	// Best-effort: set a sane terminal size so dsh doesn't see a 0x0 pty.
	_ = pty.Setsize(master, &pty.Winsize{Rows: 40, Cols: 120})
	m.pty = master
	stdout := master // scan reads the pty master

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.cmd = cmd
	m.done = make(chan struct{})
	m.running = true
	m.exit = 0
	m.errMsg = ""
	m.url = ""
	m.logf("dsh spawned pid=%d args=%v", cmd.Process.Pid, args)

	go m.scan(ctx, stdout)
	go m.wait(cmd)
	m.notifyLocked()
	return nil
}

// scan reads the pty/stdout, looking for the dsh web URL.
func (m *Manager) scan(ctx context.Context, stdout io.Reader) {
	m.logf("scan: started")
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		// Under a pty, lines end with \r\n; strip the \r so the URL matcher
		// and log lines stay clean.
		line := strings.TrimRight(sc.Text(), "\r")
		m.logf("dsh> %s", line)
		if mm := URLRe.FindStringSubmatch(line); mm != nil {
			u := mm[0]
			port := 0
			fmt.Sscanf(mm[2], "%d", &port)
			// Normalise: a bare http://host:port (local-no-auth mode prints the
			// URL without a path) should navigate to "/", not the bare origin.
			if parsed, err := url.Parse(u); err == nil && parsed.Path == "" && parsed.RawQuery == "" {
				parsed.Path = "/"
				u = parsed.String()
			}
			m.mu.Lock()
			m.url = u
			if port > 0 {
				m.port = port
			}
			onURL := m.onURL
			m.mu.Unlock()
			m.logf("dsh ready: %s", u)
			if onURL != nil {
				onURL(u, port)
			}
		}
	}
	m.mu.Lock()
	m.notifyLocked()
	m.mu.Unlock()
}

// wait reaps the process and reports exit.
func (m *Manager) wait(cmd *exec.Cmd) {
	err := cmd.Wait()
	m.mu.Lock()
	restart := false
	if m.cmd == cmd {
		m.running = false
		m.exit = cmd.ProcessState.ExitCode()
		if err != nil {
			m.errMsg = err.Error()
		}
		m.cmd = nil
		m.cancel = nil
		// Close the pty master so the scanner goroutine ends.
		if m.pty != nil {
			_ = m.pty.Close()
			m.pty = nil
		}
		// Auto-restart on unexpected exit (not a user Stop, not app shutdown).
		if m.AutoRestart && !m.stopping {
			restart = true
			m.logf("dsh exited unexpectedly (code=%d), scheduling restart", m.exit)
		}
		m.notifyLocked()
	}
	m.mu.Unlock()
	close(m.done)

	if restart {
		// Let the owner show a "restarting…" overlay before the gap, so a live
		// dsh page never stares at a dead connection. Run on a fresh goroutine
		// (we are not on the main thread here) and never under m.mu.
		if m.OnUnexpectedExit != nil {
			go m.OnUnexpectedExit()
		}
		// Back off briefly to avoid a tight crash-restart loop, then relaunch.
		time.Sleep(2 * time.Second)
		m.mu.Lock()
		stillStopping := m.stopping
		m.mu.Unlock()
		if !stillStopping {
			if err := m.Start(); err != nil {
				m.logf("auto-restart dsh failed: %v", err)
			}
		}
	}
}

// Stop terminates the child and waits for it to exit.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopping = true
	cmd := m.cmd
	done := m.done
	if cmd == nil || cmd.Process == nil {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	m.logf("stopping dsh pid=%d", cmd.Process.Pid)
	_ = cmd.Process.Kill()
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			m.logf("dsh did not exit in time")
		}
	}
}

// Restart stops and starts dsh again, preserving config.
func (m *Manager) Restart() error {
	m.mu.Lock()
	m.stopping = false // restart is intentional; the new process should auto-restart
	m.mu.Unlock()
	m.Stop()
	return m.Start()
}

// Preflight verifies that dsh can actually boot with the current configuration
// (binary / DSH_HOME / profile plugins) before committing to a restart or an
// update. It launches a throwaway dsh web instance on an OS-assigned port
// (--port 0, so it never collides with the live instance), waits for it to
// print its ready URL, and kills it. Returns nil when boot succeeded; a
// descriptive error when the process exits early, hangs, or never becomes
// ready — which is exactly what happens when an installed kernel is
// incompatible with the current profile plugins, or a plugin was edited into a
// broken state.
//
// It intentionally does not touch the running instance's state (m.cmd etc.).
func (m *Manager) Preflight(timeout time.Duration) error {
	m.mu.Lock()
	bin := m.bin
	home := m.home
	extra := append([]string(nil), m.extra...)
	m.mu.Unlock()

	if bin == "" {
		var err error
		bin, err = m.resolveBin()
		if err != nil {
			return fmt.Errorf("preflight: %w", err)
		}
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	// Probe on an OS-assigned port so it can never collide with the live dsh.
	args := []string{"--profile", "web", "--no-open", "--port", "0"}
	args = append(args, extra...)

	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	if home != "" {
		cmd.Env = append(cmd.Env, "DSH_HOME="+home)
	}
	if h, err := os.UserHomeDir(); err == nil {
		cmd.Dir = h
		cmd.Env = replaceEnv(cmd.Env, "PWD", h)
	}

	master, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("preflight start: %w", err)
	}
	defer func() {
		_ = master.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	}()
	_ = pty.Setsize(master, &pty.Winsize{Rows: 40, Cols: 120})

	ready := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(master)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			if URLRe.MatchString(line) {
				ready <- nil
				return
			}
		}
		ready <- fmt.Errorf("process exited before printing a ready URL")
	}()

	select {
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return fmt.Errorf("preflight: dsh did not become ready within %s", timeout)
	case err := <-ready:
		if err != nil {
			return fmt.Errorf("preflight: %w", err)
		}
	}
	return nil
}

// SetConfig updates the launch configuration (binary/home/port/extra). It takes
// effect on the next Start/Restart; callers typically SetConfig then Restart.
func (m *Manager) SetConfig(bin, home string, port int, extra []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bin = bin
	m.home = home
	m.port = port
	m.extra = append([]string(nil), extra...)
}

// Cleanup stops the child; safe to call from OnShutdown.
func (m *Manager) Cleanup() {
	m.Stop()
}
