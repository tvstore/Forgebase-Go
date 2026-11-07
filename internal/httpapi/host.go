package httpapi

import "strings"

// IsWellformedHost is true for a host (as the Host header leaves it, without the port) that is a
// plausible name or address: letters, digits, '-', '.' and '_', or a bracketed IPv6 address. Anything
// else is a malformed Host. The config file uses the same rule for the hosts of a site, so a host
// that could never match a request is refused when the file is loaded (Rust
// forgebase_http::is_wellformed_host).
func IsWellformedHost(host string) bool {
	if inner, ok := strings.CutPrefix(host, "["); ok {
		if inner, ok = strings.CutSuffix(inner, "]"); ok {
			if inner == "" {
				return false
			}
			for i := 0; i < len(inner); i++ {
				b := inner[i]
				if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F' || b == ':' || b == '.') {
					return false
				}
			}
			return true
		}
	}
	if host == "" {
		return false
	}
	for i := 0; i < len(host); i++ {
		b := host[i]
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '-' || b == '.' || b == '_') {
			return false
		}
	}
	return true
}
