//go:build windows

package dshproc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// spawnDsh starts cmd and returns a merged stdout+stderr stream to scan for
// the ready URL. Windows has no pseudo-terminals (creack/pty returns
// ErrUnsupported), but dsh does not need one there: the no-TTY hang is a
// macOS/launchd quirk. We merge both streams through an os.Pipe so the URL is
// found whichever stream dsh prints it to.
//
// npm-installed global bins are .cmd shims on Windows, and CreateProcess
// cannot execute a .cmd directly — so those are wrapped in `cmd /C`.
func spawnDsh(cmd *exec.Cmd) (*dshChild, error) {
	// Wrap .cmd/.bat shims (e.g. dsh.cmd from `npm i -g`) via cmd /C.
	if ext := strings.ToLower(filepath.Ext(cmd.Path)); ext == ".cmd" || ext == ".bat" {
		shim := cmd.Path
		inner := cmd.Args[1:] // drop the shim path itself
		wrapped := exec.Command("cmd", append([]string{"/C", shim}, inner...)...)
		wrapped.Env = cmd.Env
		wrapped.Dir = cmd.Dir
		cmd = wrapped
	}

	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, err
	}
	// The scanner must hit EOF when the child exits: close the parent's write
	// end. Keep the read end so a late drain can still be attempted.
	child := &dshChild{
		cmd: cmd,
		out: r,
		closeOut: func() {
			_ = w.Close()
			_ = r.Close()
		},
	}
	return child, nil
}
