package registry

import "github.com/Ohgwen/on-netreg/internal/unifi"

func makeClient(mac, name, hostname string) unifi.NetworkClient {
	return unifi.NetworkClient{
		MAC:      mac,
		Name:     name,
		Hostname: hostname,
		IP:       "192.168.1.100",
		Online:   true,
	}
}
