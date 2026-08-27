// Package registry contains the pure hostname-derivation and
// reconciliation logic that decides what DNS state should exist for a
// given set of UniFi clients. It has no network or database dependencies
// so it can be exercised directly in unit tests.
package registry

import (
	"regexp"
	"strings"

	"github.com/Ohgwen/on-netreg/internal/macaddr"
	"github.com/Ohgwen/on-netreg/internal/unifi"
)

var labelInvalidChars = regexp.MustCompile(`[^a-z0-9-]+`)

// SanitizeLabel converts s into a valid DNS label: lowercase, alphanumeric
// and hyphen only, no leading/trailing/duplicate hyphens, max 63 chars.
func SanitizeLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = labelInvalidChars.ReplaceAllString(s, "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	return s
}

// genericNames are client-reported names that carry no useful identity
// and should be treated the same as a blank name.
var genericNames = map[string]bool{
	"unknown":        true,
	"android":        true,
	"android-device": true,
	"iphone":         true,
	"ipad":           true,
	"device":         true,
	"esp32":          true,
	"esp8266":        true,
	"espressif":      true,
	"localhost":      true,
}

func isGenericName(name string) bool {
	return genericNames[strings.ToLower(strings.TrimSpace(name))]
}

// Resolve derives the DNS hostname for a client: an explicit override wins,
// then the sanitized UniFi-reported name/hostname (if present and not
// generic), then a generated vendor+MAC-suffix fallback.
func Resolve(client unifi.NetworkClient, override *string, fallbackPattern string) string {
	if override != nil {
		if s := SanitizeLabel(*override); s != "" {
			return s
		}
	}
	for _, candidate := range []string{client.Name, client.Hostname} {
		if candidate == "" || isGenericName(candidate) {
			continue
		}
		if s := SanitizeLabel(candidate); s != "" {
			return s
		}
	}
	return fallbackName(client.MAC, fallbackPattern)
}

func fallbackName(mac, pattern string) string {
	if pattern == "" {
		pattern = "{vendor}-{macsuffix}"
	}
	vendor := VendorFor(mac)
	suffix := macaddr.Suffix(mac, 4)
	name := strings.NewReplacer("{vendor}", vendor, "{macsuffix}", suffix).Replace(pattern)
	if s := SanitizeLabel(name); s != "" {
		return s
	}
	return "device-" + suffix
}

// Disambiguate returns name unchanged if it's unclaimed or already owned by
// mac in taken; otherwise it appends a MAC-derived suffix to make it
// unique.
func Disambiguate(name, mac string, taken map[string]string) string {
	if owner, ok := taken[name]; !ok || owner == mac {
		return name
	}
	suffixed := name + "-" + macaddr.Suffix(mac, 4)
	if owner, ok := taken[suffixed]; !ok || owner == mac {
		return suffixed
	}
	return name + "-" + macaddr.Suffix(mac, 12)
}
