package technitium

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Value is the record's data as one line of text, e.g. the address of an A
// record or the target of a CNAME.
func (r DNSRecord) Value() string {
	str := func(key string) string {
		v, _ := r.RData[key].(string)
		return v
	}
	switch strings.ToUpper(r.Type) {
	case "A", "AAAA":
		return str("ipAddress")
	case "CNAME":
		return str("cname")
	case "PTR":
		return str("ptrName")
	case "TXT":
		return str("text")
	case "NS":
		return str("nameServer")
	case "MX":
		return fmt.Sprintf("%v %s", r.RData["preference"], str("exchange"))
	case "SRV":
		return fmt.Sprintf("%v %v %v %s", r.RData["priority"], r.RData["weight"], r.RData["port"], str("target"))
	case "SOA":
		return fmt.Sprintf("%s %s serial %v", str("primaryNameServer"), str("responsiblePerson"), r.RData["serial"])
	}
	keys := make([]string, 0, len(r.RData))
	for k := range r.RData {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, r.RData[k]))
	}
	return strings.Join(parts, " ")
}

// Deletable reports whether DeleteRecord can identify this record from its
// name, type and Value alone.
func (r DNSRecord) Deletable() bool {
	switch strings.ToUpper(r.Type) {
	case "A", "AAAA", "CNAME", "PTR", "TXT":
		return true
	}
	return false
}

// DeleteRequest builds the request that removes exactly this record from
// zone (blank lets the server pick the closest zone).
func (r DNSRecord) DeleteRequest(zone string) DeleteRecordRequest {
	return NewDeleteRequest(r.Name, zone, r.Type, r.Value())
}

// NewDeleteRequest builds the request that removes the record of type
// rtype at domain holding value (as DNSRecord.Value renders it).
func NewDeleteRequest(domain, zone, rtype, value string) DeleteRecordRequest {
	req := DeleteRecordRequest{Domain: domain, Zone: zone, Type: strings.ToUpper(rtype)}
	switch req.Type {
	case "A", "AAAA":
		req.IPAddress = value
	case "PTR":
		req.PTRName = value
	case "TXT":
		req.Text = value
	}
	return req
}

// Modified is when the record last changed, or the zero time if the server
// didn't report it.
func (r DNSRecord) Modified() time.Time { return parseTimestamp(r.LastModified) }

// LastUsed is when the server last answered a query with this record, or
// the zero time if never (or not reported).
func (r DNSRecord) LastUsed() time.Time { return parseTimestamp(r.LastUsedOn) }

func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			if t.Year() <= 1 {
				return time.Time{}
			}
			return t
		}
	}
	return time.Time{}
}
