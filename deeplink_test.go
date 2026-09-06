package main

import "testing"

func TestDeepLinkTarget(t *testing.T) {
	base := "http://127.0.0.1:3080/?token=abc"
	cases := []struct {
		raw    string
		want   string
		wantOK bool
	}{
		{"dsh://", "http://127.0.0.1:3080/", true},
		{"dsh://session/sess_123", "http://127.0.0.1:3080/?session=sess_123", true},
		{"dsh://workspace/ws1/session/sess_9", "http://127.0.0.1:3080/?session=sess_9&workspace=ws1", true},
		{"dsh://workspace/ws1", "http://127.0.0.1:3080/?workspace=ws1", true},
		{"dsh://?session=sess_7", "http://127.0.0.1:3080/?session=sess_7", true},
		// raw query passthrough keeps the caller's order verbatim
		{"dsh://?workspace=w2&session=s2", "http://127.0.0.1:3080/?workspace=w2&session=s2", true},
		// session id needs URL escaping
		{"dsh://session/ab cd", "http://127.0.0.1:3080/?session=ab+cd", true},
		// unknown scheme is not a deep link
		{"http://127.0.0.1:3080/?session=x", "", false},
		// no dsh yet
		{"dsh://session/s", "", false},
	}
	for _, c := range cases {
		var dshBase = base
		if c.raw == "dsh://session/s" {
			dshBase = "" // simulate dsh not ready
		}
		got, ok := deepLinkTarget(c.raw, dshBase)
		if ok != c.wantOK {
			t.Errorf("deepLinkTarget(%q, %q) ok=%v want %v", c.raw, dshBase, ok, c.wantOK)
			continue
		}
		if ok && got != c.want {
			t.Errorf("deepLinkTarget(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}
