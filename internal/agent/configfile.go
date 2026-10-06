package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type cniConfig struct {
	CNIVersion string          `json:"cniVersion"`
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	VRFName    string          `json:"vrfName"`
	VRFTable   int             `json:"vrfTable"`
	IPAM       hostLocalConfig `json:"ipam"`
}

type hostLocalConfig struct {
	Type    string          `json:"type"`
	Ranges  [][]rangeConfig `json:"ranges"`
	DataDir string          `json:"dataDir"`
}

type rangeConfig struct {
	Subnet string `json:"subnet"`
}

func WriteCNIConfig(path, podCIDR, vrfName string, vrfTable int) error {
	if path == "" || podCIDR == "" {
		return fmt.Errorf("CNI config path and PodCIDR are required")
	}
	conf := cniConfig{
		CNIVersion: "1.1.0",
		Name:       "netkit-pods",
		Type:       "netkit-cni",
		VRFName:    vrfName,
		VRFTable:   vrfTable,
		IPAM: hostLocalConfig{
			Type:    "host-local",
			Ranges:  [][]rangeConfig{{{Subnet: podCIDR}}},
			DataDir: "/var/lib/cni/netkit-cni",
		},
	}
	data, err := json.MarshalIndent(conf, "", "  ")
	if err != nil {
		return fmt.Errorf("encode CNI configuration: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create CNI config directory %s: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, ".netkit-cni-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary CNI config: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return fmt.Errorf("set temporary CNI config permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary CNI config: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync temporary CNI config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary CNI config: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("atomically replace CNI config %s: %w", path, err)
	}
	return nil
}
