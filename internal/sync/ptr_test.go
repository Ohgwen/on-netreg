package sync

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Ohgwen/on-netreg/internal/db"
)

func TestRepairPTRsFixesOnlyStaleRecords(t *testing.T) {
	gdb := testDB(t)
	seedAppSettings(t, gdb)
	seedTechnitium(t, gdb)

	now := time.Now()
	devices := []db.Device{
		// PTR already right: left alone.
		{MAC: "10:00:00:00:00:01", Hostname: "on-db-01", Zone: "infra.example", IPAddress: "10.0.0.1", DNSRecordSynced: true},
		// Renamed before updates carried the PTR: rewritten.
		{MAC: "10:00:00:00:00:02", Hostname: "on-util-01", Zone: "infra.example", IPAddress: "10.0.0.2", DNSRecordSynced: true},
		// No PTR at all: written.
		{MAC: "10:00:00:00:00:03", Hostname: "cam", Zone: "sec.example", IPAddress: "10.0.0.3", DNSRecordSynced: true},
		// Two published names on one address: skipped as a conflict.
		{MAC: "10:00:00:00:00:04", Hostname: "a", Zone: "lan.example", IPAddress: "10.0.0.4", DNSRecordSynced: true},
		{MAC: "10:00:00:00:00:05", Hostname: "b", Zone: "lan.example", IPAddress: "10.0.0.4", DNSRecordSynced: true},
		// Not published: ignored.
		{MAC: "10:00:00:00:00:06", Hostname: "off", Zone: "lan.example", IPAddress: "10.0.0.6"},
		{MAC: "10:00:00:00:00:07", Hostname: "excl", Zone: "lan.example", IPAddress: "10.0.0.7", DNSRecordSynced: true, Excluded: true},
	}
	for i := range devices {
		devices[i].FirstSeen, devices[i].LastSeen = now, now
		if err := gdb.Create(&devices[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	current := map[string][]string{
		"10.0.0.1": {"on-db-01.infra.example"},
		"10.0.0.2": {"ubuntu-misc.infra.example"},
	}
	var looked []string
	dns := &fakeDNS{}
	e := newTestEngine(gdb, dns, nil)
	e.LookupPTR = func(ctx context.Context, dnsHost, ip string, timeout time.Duration) ([]string, error) {
		looked = append(looked, ip)
		return current[ip], nil
	}

	report, err := e.RepairPTRs(context.Background())
	if err != nil {
		t.Fatalf("RepairPTRs: %v", err)
	}

	want := map[string]string{"10.0.0.2": "on-util-01.infra.example", "10.0.0.3": "cam.sec.example"}
	if len(dns.ptrCalls) != len(want) {
		t.Fatalf("PTR writes = %v, want %v", dns.ptrCalls, want)
	}
	for ip, name := range want {
		if dns.ptrCalls[ip] != name {
			t.Errorf("PTR %s = %q, want %q", ip, dns.ptrCalls[ip], name)
		}
	}
	sort.Strings(looked)
	if strings.Join(looked, ",") != "10.0.0.1,10.0.0.2,10.0.0.3" {
		t.Errorf("looked up %v", looked)
	}
	if report.Checked != 3 || len(report.Fixed) != 2 || len(report.Conflicts) != 1 || len(report.Errors) != 0 {
		t.Errorf("report = %+v", report)
	}
}
