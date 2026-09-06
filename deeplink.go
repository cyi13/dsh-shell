package main

import (
	"net/url"
	"strings"
)

// Deep link support: the desktop shell registers the `dsh://` URL scheme so an
// external caller (a VS Code extension, a browser link, `open dsh://session/…`)
// can wake the shell and jump straight to a dsh web session.
//
// dsh web itself deep-links via URL parameters (?session=…, optional
// &workspace=…) handled by the dsh-deeplink profile plugin. So a dsh:// URL is
// translated into the matching dsh web http(s) URL and the window is navigated
// to it.
//
// Supported forms:
//
//	dsh://session/<sessionId>
//	dsh://workspace/<workspaceId>/session/<sessionId>
//	dsh://?session=<sessionId>[&workspace=<workspaceId>]   (raw query passthrough)
//	dsh://<path or anything else>                          (appended to the web base)
//	dsh://                                                   (just wake to the dsh home)
func deepLinkTarget(raw, dshBase string) (string, bool) {
	if dshBase == "" {
		return "", false
	}
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "dsh://") {
		return "", false
	}
	rest := raw[len("dsh://"):]

	base, err := url.Parse(dshBase)
	if err != nil || base.Host == "" {
		return "", false
	}

	// Preserve the origin; drop any existing path/query/fragment of the base
	// (the dsh home URL is normally http://host:port/ or http://host:port/?token=…).
	cleanBase := &url.URL{Scheme: base.Scheme, Host: base.Host}

	if rest == "" {
		cleanBase.Path = "/"
		return cleanBase.String(), true
	}

	// Raw query passthrough: dsh://?session=x&workspace=y
	if strings.HasPrefix(rest, "?") {
		cleanBase.Path = "/"
		cleanBase.RawQuery = strings.TrimPrefix(rest, "?")
		return cleanBase.String(), true
	}

	segs := strings.Split(strings.Trim(rest, "/"), "/")
	build := func(q url.Values) (string, bool) {
		cleanBase.Path = "/"
		cleanBase.RawQuery = q.Encode()
		return cleanBase.String(), true
	}

	switch segs[0] {
	case "session":
		if len(segs) >= 2 && segs[1] != "" {
			q := url.Values{}
			q.Set("session", segs[1])
			return build(q)
		}
	case "workspace":
		if len(segs) >= 2 && segs[1] != "" {
			q := url.Values{}
			q.Set("workspace", segs[1])
			if len(segs) >= 4 && segs[2] == "session" && segs[3] != "" {
				q.Set("session", segs[3])
			}
			return build(q)
		}
	}

	// Fallback: treat the rest as a path on the dsh web origin.
	cleanBase.Path = "/" + strings.Trim(rest, "/")
	return cleanBase.String(), true
}
