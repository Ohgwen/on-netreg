package registry

import (
	"strings"

	"github.com/Ohgwen/on-netreg/internal/macaddr"
)

// ouiVendors is a small, hand-curated subset of common IEEE OUI prefixes
// (uppercase, no separators) used only for the naming fallback pattern.
// It is not meant to be exhaustive -- unrecognized prefixes just fall back
// to the generic "device" vendor name. Extend as needed.
var ouiVendors = map[string]string{
	"F4F5D8": "google",
	"3C5AB4": "google",
	"A4772B": "amazon",
	"68370A": "amazon",
	"FCA13E": "amazon",
	"DCA632": "raspberrypi",
	"B827EB": "raspberrypi",
	"E45F01": "raspberrypi",
	"D83ADD": "ubiquiti",
	"245A4C": "ubiquiti",
	"04D6AA": "ubiquiti",
	"7483C2": "espressif",
	"CC50E3": "espressif",
	"A020A6": "espressif",
	"3C6105": "apple",
	"A4B197": "apple",
	"F0189F": "apple",
	"E4E4AB": "apple",
	"BC9FEF": "samsung",
	"E8508B": "samsung",
	"5C0A5B": "samsung",
	"D0037F": "intel",
	"001B21": "intel",
	"3417EB": "dell",
	"18DBF2": "dell",
	"D8D385": "hp",
	"3C4A92": "hp",
	"B0BE76": "microsoft",
	"7CED8D": "microsoft",
	"288029": "nintendo",
	"9C2AA8": "sonos",
}

// VendorFor returns a short, DNS-safe vendor name for the given MAC's OUI,
// or "device" if unrecognized.
func VendorFor(mac string) string {
	oui := strings.ToUpper(macaddr.OUI(mac))
	if v, ok := ouiVendors[oui]; ok {
		return v
	}
	return "device"
}
