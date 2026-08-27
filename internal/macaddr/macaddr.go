// Package macaddr normalizes MAC address strings to a single canonical
// form so the same device is recognized regardless of how a source
// formats it.
package macaddr

import "strings"

// Normalize lowercases mac and converts any '-' separators to ':', so
// "AA-BB-CC-DD-EE-FF" and "aa:bb:cc:dd:ee:ff" compare equal.
func Normalize(mac string) string {
	mac = strings.ToLower(strings.TrimSpace(mac))
	mac = strings.ReplaceAll(mac, "-", ":")
	return mac
}

// Suffix returns the last n hex characters of the MAC address (ignoring
// separators), for use in generated hostnames.
func Suffix(mac string, n int) string {
	stripped := strings.ReplaceAll(strings.ReplaceAll(mac, ":", ""), "-", "")
	if len(stripped) <= n {
		return stripped
	}
	return stripped[len(stripped)-n:]
}

// OUI returns the first 6 hex characters (3 bytes) of the MAC address,
// ignoring separators, which identifies the manufacturer.
func OUI(mac string) string {
	stripped := strings.ReplaceAll(strings.ReplaceAll(mac, ":", ""), "-", "")
	if len(stripped) < 6 {
		return stripped
	}
	return stripped[:6]
}
