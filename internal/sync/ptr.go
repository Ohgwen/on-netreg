package sync

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/registry"
	"github.com/Ohgwen/on-netreg/internal/settings"
	"github.com/Ohgwen/on-netreg/internal/technitium"
)

// ptrLookupTimeout bounds each reverse lookup made while repairing PTRs.
const ptrLookupTimeout = 3 * time.Second

// PTRReport summarizes a RepairPTRs run.
type PTRReport struct {
	// Checked is how many published addresses had their PTR looked up.
	Checked int
	// Fixed lists the "ip -> name" PTRs that were rewritten.
	Fixed []string
	// Conflicts lists addresses more than one published name claims; their
	// PTR is left alone since there's no single right answer.
	Conflicts []string
	// Errors lists lookups or writes that failed.
	Errors []string
}

// RepairPTRs points the reverse (PTR) record of every published address at
// the name netreg currently publishes for it. PTRs are only written when
// they differ, so a run over a healthy zone makes no changes. It covers
// records renamed before updates carried the PTR along, and any reverse
// entry edited by hand.
func (e *Engine) RepairPTRs(ctx context.Context) (PTRReport, error) {
	e.runMu.Lock()
	defer e.runMu.Unlock()

	var report PTRReport

	dnsCfg, err := settings.LoadTechnitium(e.DB, e.SecretKey)
	if err != nil {
		return report, fmt.Errorf("loading technitium settings: %w", err)
	}
	dns := e.NewDNSClient(dnsCfg)
	dnsHost := dnsCfg.VerifyDNSServer
	if dnsHost == "" {
		dnsHost = technitium.HostFromBaseURL(dnsCfg.BaseURL)
	}

	names, err := e.publishedNames()
	if err != nil {
		return report, err
	}

	ips := make([]string, 0, len(names))
	for ip := range names {
		ips = append(ips, ip)
	}
	sort.Strings(ips)

	for _, ip := range ips {
		claimants := names[ip]
		if len(claimants) > 1 {
			report.Conflicts = append(report.Conflicts, ip+" ("+strings.Join(claimants, ", ")+")")
			continue
		}
		want := claimants[0]

		report.Checked++
		current, err := e.lookupPTR(ctx, dnsHost, ip)
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
			continue
		}
		if len(current) == 1 && strings.EqualFold(current[0], want) {
			continue
		}

		err = dns.SetPTR(ctx, ip, want, dnsCfg.TTL)
		e.ptrEvent(ip, want, current, err)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s -> %s: %v", ip, want, err))
			continue
		}
		report.Fixed = append(report.Fixed, ip+" -> "+want)
	}
	return report, nil
}

// publishedNames maps each address netreg has a confirmed A record for to
// the FQDN(s) published at it: device records plus identities' shared ones.
func (e *Engine) publishedNames() (map[string][]string, error) {
	out := make(map[string][]string)
	add := func(ip, label, zone string) {
		if !registry.HasValidIP(ip) || label == "" || zone == "" {
			return
		}
		out[ip] = append(out[ip], fqdn(label, zone))
	}

	var devices []db.Device
	if err := e.DB.Where("dns_record_synced = ? AND excluded = ? AND identity_id IS NULL", true, false).Find(&devices).Error; err != nil {
		return nil, fmt.Errorf("loading devices: %w", err)
	}
	for _, d := range devices {
		// Hostname, not EffectiveHostname: it's the name actually in DNS.
		add(d.IPAddress, d.Hostname, d.Zone)
	}

	var identities []db.Identity
	if err := e.DB.Where("dns_record_synced = ?", true).Find(&identities).Error; err != nil {
		return nil, fmt.Errorf("loading identities: %w", err)
	}
	for _, i := range identities {
		add(i.IPAddress, i.SyncedHostname, i.Zone)
	}
	return out, nil
}

func (e *Engine) lookupPTR(ctx context.Context, dnsHost, ip string) ([]string, error) {
	if e.LookupPTR != nil {
		return e.LookupPTR(ctx, dnsHost, ip, ptrLookupTimeout)
	}
	return technitium.LookupPTR(ctx, dnsHost, ip, ptrLookupTimeout)
}

func (e *Engine) ptrEvent(ip, name string, previous []string, applyErr error) {
	detail := fmt.Sprintf("PTR %s -> %s", ip, name)
	if len(previous) > 0 {
		detail += " (was " + strings.Join(previous, ", ") + ")"
	}
	if applyErr != nil {
		e.Logger.Error("failed to repair PTR", "ip", ip, "name", name, "error", applyErr)
		detail += ": " + applyErr.Error()
	}
	event := db.SyncEvent{
		Action:    db.SyncEventPTR,
		Detail:    detail,
		Success:   applyErr == nil,
		Actor:     db.SystemActor,
		CreatedAt: time.Now(),
	}
	if err := e.DB.Create(&event).Error; err != nil {
		e.Logger.Error("failed to record sync event", "error", err)
	}
}
