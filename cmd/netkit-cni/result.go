package main

import (
	"fmt"

	current "github.com/containernetworking/cni/pkg/types/100"
)

func podIPv4(result *current.Result) (*current.IPConfig, error) {
	var found *current.IPConfig
	for _, address := range result.IPs {
		if address == nil {
			return nil, fmt.Errorf("IPAM result contains a null IP entry")
		}
		if address.Address.IP.To4() == nil {
			return nil, fmt.Errorf("IPv6 address %s is unsupported", address.Address.String())
		}
		if found != nil {
			return nil, fmt.Errorf("expected exactly one IPv4 address, got more than one")
		}
		found = address.Copy()
	}
	if found == nil {
		return nil, fmt.Errorf("IPAM returned no IPv4 address")
	}
	return found, nil
}
