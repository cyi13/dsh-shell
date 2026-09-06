//go:build !windows

package dshproc

import (
	"fmt"
	"os/exec"

	"github.com/creack/pty"
)

// spawnDsh starts cmd with its stdout/stderr attached to a pseudo-terminal and
// returns the pty master to scan for the ready URL. dsh hangs during startup
// when it has no controlling terminal (as happens when spawned from a
// GUI-launched app on macOS: no TTY, stdin=/dev/null), so on Unix-like
// platforms we always give it one.
func spawnDsh(cmd *exec.Cmd) (*dshChild, error) {
	master, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start dsh (pty): %w", err)
	}
	// Best-effort: set a sane terminal size so dsh doesn't see a 0x0 pty.
	_ = pty.Setsize(master, &pty.Winsize{Rows: 40, Cols: 120})
	return &dshChild{
		cmd: cmd,
		out: master,
		closeOut: func() {
			_ = master.Close()
		},
	}, nil
}
