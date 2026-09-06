package updater

import (
	"context"
	"testing"
)

func TestSemverGt(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.2", "0.1.1", true},
		{"0.1.1", "0.1.2", false},
		{"0.1.2-alpha.4", "0.1.2", false}, // prerelease < release
		{"0.1.2", "0.1.2-alpha.4", true},  // release > prerelease
		{"0.1.2-alpha.4", "0.1.2-alpha.3", true},
		{"0.1.1-rc.2", "0.1.2-alpha.4", false}, // alpha.4 has a HIGHER core (0.1.2) than rc.2 (0.1.1)
		{"0.2.0", "0.1.9", true},
		{"1.0.0", "0.9.9", true},
		{"0.1.2-alpha.4", "0.1.2-alpha.4", false}, // equal
		{"0.1.2", "0.1.2", false},                 // equal
	}
	for _, c := range cases {
		if got := semverGt(c.a, c.b); got != c.want {
			t.Errorf("semverGt(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestResolveInstallVersion verifies the installer targets the version the
// channel-aware check selected — not the `latest` dist-tag (which may be an
// older stable that would downgrade an alpha-tracked install).
func TestResolveInstallVersion(t *testing.T) {
	u := New("", nil)
	// Simulate a completed alpha-channel check that found 0.1.2-alpha.5.
	u.mu.Lock()
	u.state.LatestVersion = "0.1.2-alpha.5"
	u.mu.Unlock()

	got, err := u.resolveInstallVersion(context.Background())
	if err != nil {
		t.Fatalf("resolveInstallVersion: %v", err)
	}
	if got != "0.1.2-alpha.5" {
		t.Errorf("resolveInstallVersion = %q, want 0.1.2-alpha.5 (the checked alpha version, NOT @latest)", got)
	}
}
