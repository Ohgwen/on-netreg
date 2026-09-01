// Package netcheck provides a best-effort "is this IP up" liveness probe,
// used to decide which member of a multi-MAC Identity currently backs the
// shared DNS record.
package netcheck

import (
	"context"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

// fallbackPorts is tried via a raw TCP dial when the system ping binary is
// unavailable or fails to run at all (as opposed to reporting the host
// down, which is a normal "not alive" result).
var fallbackPorts = []int{443, 80, 22, 3389}

// IsAlive reports whether ip responds to a single ICMP echo (via the
// system `ping` binary) within timeout. If ping can't be run at all (e.g.
// missing binary, no permission), it falls back to a short TCP dial
// against a handful of common ports and treats any successful connect as
// "alive".
func IsAlive(ctx context.Context, ip string, timeout time.Duration) bool {
	ok, ran := pingAlive(ctx, ip, timeout)
	if ran {
		return ok
	}
	return tcpAlive(ctx, ip, timeout)
}

// New returns an IsAlive closure with a fixed timeout, matching the
// func(ip string) bool shape registry.SelectActive expects.
func New(timeout time.Duration) func(ip string) bool {
	return func(ip string) bool {
		return IsAlive(context.Background(), ip, timeout)
	}
}

func pingAlive(ctx context.Context, ip string, timeout time.Duration) (alive bool, ran bool) {
	var args []string
	secs := int(timeout.Seconds())
	if secs < 1 {
		secs = 1
	}
	if runtime.GOOS == "windows" {
		args = []string{"-n", "1", "-w", strconv.Itoa(secs * 1000), ip}
	} else {
		args = []string{"-c", "1", "-W", strconv.Itoa(secs), ip}
	}

	cctx, cancel := context.WithTimeout(ctx, timeout+time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ping", args...)
	err := cmd.Run()
	if err == nil {
		return true, true
	}
	if _, ok := err.(*exec.ExitError); ok {
		// ping ran and reported the host unreachable/down.
		return false, true
	}
	// Binary missing, permission denied, etc. -- couldn't run it at all.
	return false, false
}

func tcpAlive(ctx context.Context, ip string, timeout time.Duration) bool {
	for _, port := range fallbackPorts {
		d := net.Dialer{Timeout: timeout}
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		if err == nil {
			conn.Close()
			return true
		}
	}
	return false
}
