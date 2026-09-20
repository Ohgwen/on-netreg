// Package macaddr normalizes MAC address strings to a single canonical
// form so the same device is recognized regardless of how a source
// formats it.
package macaddr

import (
	"strconv"
	"strings"
)

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

// IsPrivate reports whether mac is a locally administered address rather
// than one assigned from a manufacturer's registered OUI. Modern OSes
// (iOS, Android, Windows) generate a random locally administered MAC per
// network by default for privacy ("private Wi-Fi address"), identifiable
// by the U/L bit -- the second-least-significant bit of the first octet --
// being set to 1. Such addresses carry no meaningful vendor identity and
// are typically reassigned on every reconnect.
func IsPrivate(mac string) bool {
	oui := OUI(mac)
	if len(oui) < 2 {
		return false
	}
	firstByte, err := strconv.ParseUint(oui[:2], 16, 8)
	if err != nil {
		return false
	}
	return firstByte&0x02 != 0
}
