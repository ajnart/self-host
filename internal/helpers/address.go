package helpers

import (
	"net/url"
	"strconv"
	"strings"
)

// HTTPAddress parses an absolute http or https address without
// credentials, query, fragment, escaped path or stray characters, whose
// port, when present, is a decimal from 1 to 65535.
func HTTPAddress(value string) (*url.URL, bool) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || strings.HasSuffix(u.Host, ":") || strings.ContainsAny(value, " \t\r\n\\$#") {
		return nil, false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strings.Trim(port, "0123456789") != "" {
			return nil, false
		}
	}
	return u, true
}

// HTTPOrigin is HTTPAddress restricted to an origin: no path beyond "/".
func HTTPOrigin(value string) (*url.URL, bool) {
	u, ok := HTTPAddress(value)
	if !ok || u.Path != "" && u.Path != "/" {
		return nil, false
	}
	return u, true
}
