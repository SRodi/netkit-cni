package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCNIConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "10-netkit.conflist")
	if err := WriteCNIConfig(path, "10.244.1.0/24", "nk-vrf", 100); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"cniVersion": "1.1.0"`, `"subnet": "10.244.1.0/24"`, `"dataDir": "/var/lib/cni/netkit-cni"`} {
		if !strings.Contains(text, want) {
			t.Errorf("configuration does not contain %s:\n%s", want, text)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode is %o", info.Mode().Perm())
	}
}
