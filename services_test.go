package main

import (
	"strings"
	"testing"
)

// TestNavigateJSRecovery verifies navigateJS: it must remove the restarting
// overlay, force a reload when already on the exact target URL (same-port
// restart with unchanged token), and otherwise location.replace.
func TestNavigateJSRecovery(t *testing.T) {
	js := navigateJS("http://127.0.0.1:3080/?token=abc")
	for _, want := range []string{
		"dsh-desktop-restarting",   // overlay removal
		"location.href===target",   // same-URL detection
		"location.reload()",        // same URL -> reload
		"location.replace(target)", // different URL -> replace
	} {
		if !strings.Contains(js, want) {
			t.Errorf("navigateJS missing %q\n%s", want, js)
		}
	}
}

// TestNavigateChangedJS verifies navigateChangedJS: it only navigates when the
// current URL differs (ShowDsh must not reload an already-live dsh page).
func TestNavigateChangedJS(t *testing.T) {
	js := navigateChangedJS("http://127.0.0.1:3080/")
	for _, want := range []string{
		"dsh-desktop-restarting",
		"location.href!==target",
		"location.replace(target)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("navigateChangedJS missing %q\n%s", want, js)
		}
	}
	if strings.Contains(js, "location.reload()") {
		t.Errorf("navigateChangedJS must not force a reload")
	}
}

// TestNavigateJSQuoting verifies a URL with characters that need escaping in a
// JS string literal is quoted safely (no broken/escapable syntax).
func TestNavigateJSQuoting(t *testing.T) {
	url := `http://127.0.0.1:3080/?token=a"b\c&x=1`
	js := navigateJS(url)
	if !strings.Contains(js, `a\"b\\c&x=1`) {
		t.Errorf("navigateJS did not escape URL properly:\n%s", js)
	}
	// The raw (unescaped) double quote must not appear inside the generated
	// script: strconv.Quote escapes it, otherwise the JS literal breaks.
	if strings.Contains(js, `?token=a"b`) {
		t.Errorf("navigateJS leaked an unescaped quote:\n%s", js)
	}
}

// TestNavigateFromSplashJS verifies the retry helper only acts while the page
// is still on the local splash (wails://) and is a no-op once it is a real
// http(s) dsh page — so a restart never triggers a second full load.
func TestNavigateFromSplashJS(t *testing.T) {
	js := navigateFromSplashJS("http://127.0.0.1:3080/?token=abc")
	for _, want := range []string{
		"location.protocol==='http:'||location.protocol==='https:'",
		"location.replace(target)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("navigateFromSplashJS missing %q\n%s", want, js)
		}
	}
	if strings.Contains(js, "location.reload()") {
		t.Errorf("navigateFromSplashJS must never reload")
	}
}

// TestRestartOverlayJS verifies the overlay script only targets http(s) pages
// (never the wails:// splash) and is idempotent.
func TestRestartOverlayJS(t *testing.T) {
	for _, want := range []string{
		"location.protocol !== 'http:' && location.protocol !== 'https:'",
		"dsh-desktop-restarting",
		"dsh 正在重启",
	} {
		if !strings.Contains(restartOverlayJS, want) {
			t.Errorf("restartOverlayJS missing %q", want)
		}
	}
}
