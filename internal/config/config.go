package config

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/containernetworking/cni/pkg/types"
)

const (
	DefaultVRFName  = "nk-vrf"
	DefaultVRFTable = 100
)

type IPAM struct {
	Type string `json:"type"`
}

type Network struct {
	types.NetConf
	VRFName  string `json:"vrfName,omitempty"`
	VRFTable int    `json:"vrfTable,omitempty"`
	IPAM     IPAM   `json:"ipam"`
}

func Parse(data []byte) (*Network, error) {
	var conf Network
	if err := json.Unmarshal(data, &conf); err != nil {
		return nil, fmt.Errorf("decode network configuration: %w", err)
	}
	if conf.VRFName == "" {
		conf.VRFName = DefaultVRFName
	}
	if conf.VRFTable == 0 {
		conf.VRFTable = DefaultVRFTable
	}
	if err := conf.Validate(); err != nil {
		return nil, err
	}
	return &conf, nil
}

func (c *Network) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("network name is required")
	}
	if c.Type != "netkit-cni" {
		return fmt.Errorf("type must be netkit-cni, got %q", c.Type)
	}
	if c.CNIVersion != "1.0.0" && c.CNIVersion != "1.1.0" {
		return fmt.Errorf("unsupported cniVersion %q: supported versions are 1.0.0 and 1.1.0", c.CNIVersion)
	}
	if c.IPAM.Type == "" {
		return fmt.Errorf("ipam.type is required")
	}
	if len(c.VRFName) == 0 || len(c.VRFName) > 15 {
		return fmt.Errorf("vrfName must contain 1 to 15 bytes")
	}
	if c.VRFTable <= 0 {
		return fmt.Errorf("vrfTable must be positive")
	}
	return nil
}

func IPv4HostPrefix(ip net.IP) (*net.IPNet, error) {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("%q is not an IPv4 address", ip)
	}
	return &net.IPNet{IP: append(net.IP(nil), ip4...), Mask: net.CIDRMask(32, 32)}, nil
}
