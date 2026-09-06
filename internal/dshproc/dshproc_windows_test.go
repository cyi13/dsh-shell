//go:build windows

package dshproc

import (
	"bufio"
	"os/exec"
	"strings"
	"testing"
)

// TestSpawnDshWindowsPipe verifies the Windows spawn path (merged os.Pipe)
// delivers lines that the URL scanner can match — using `cmd /C echo`, which
// exercises both the .cmd-style process and the pipe read-back.
func TestSpawnDshWindowsPipe(t *testing.T) {
	cmd := exec.Command("cmd", "/C", "echo dsh web: http://127.0.0.1:39877/?token=winpipe")
	child, err := spawnDsh(cmd)
	if err != nil {
		t.Fatalf("spawnDsh: %v", err)
	}
	defer child.closeOut()

	got := ""
	sc := bufio.NewScanner(child.out)
	if sc.Scan() {
		got = strings.TrimRight(sc.Text(), "\r")
	}
	if !URLRe.MatchString(got) {
		t.Fatalf("expected a URL line, got %q", got)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}
