package dshproc

import (
	"io"
	"os/exec"
)

// dshChild is a started dsh child process plus the stream to scan for its
// ready URL. closeOut releases the output stream (closing it makes the scanner
// hit EOF); it is safe to call multiple times. The platform-specific spawnDsh
// (child_unix.go / child_windows.go) builds one.
type dshChild struct {
	cmd      *exec.Cmd
	out      io.Reader
	closeOut func()
}
