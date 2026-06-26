package connector

import (
	"regexp"
	"strings"
)

var linkSegRE = regexp.MustCompile(`<([^>]+)>\s*;\s*(.*)`)

// NextLink returns the URL of the rel="next" entry in a Link header, or the
// empty string if absent. A results="false" parameter (Sentry pagination
// sentinel) also returns empty — the caller's for-loop exits in both cases.
func NextLink(header string) string {
	if header == "" {
		return ""
	}
	for _, seg := range splitLinkHeader(header) {
		m := linkSegRE.FindStringSubmatch(seg)
		if len(m) != 3 {
			continue
		}
		urlStr, params := m[1], m[2]
		if !linkParamEquals(params, "rel", "next") {
			continue
		}
		if linkParamEquals(params, "results", "false") {
			return ""
		}
		return urlStr
	}
	return ""
}

// splitLinkHeader splits a Link header on commas that separate segments
// without disturbing commas inside angle brackets.
func splitLinkHeader(h string) []string {
	var out []string
	depth := 0
	last := 0
	for i, r := range h {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(h[last:i]))
				last = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(h[last:]))
	return out
}

func linkParamEquals(params, key, want string) bool {
	for _, p := range strings.Split(params, ";") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(p[:eq])
		v := strings.Trim(strings.TrimSpace(p[eq+1:]), `"`)
		if k == key && v == want {
			return true
		}
	}
	return false
}
