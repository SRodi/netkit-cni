package config

import (
	"net"
	"testing"
)

func TestParseDefaults(t *testing.T) {
	conf, err := Parse([]byte(`{"cniVersion":"1.1.0","name":"pods","type":"netkit-cni","ipam":{"type":"host-local"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if conf.VRFName != DefaultVRFName || conf.VRFTable != DefaultVRFTable {
		t.Fatalf("unexpected defaults: %#v", conf)
	}
}

func TestParseRejectsInvalidConfiguration(t *testing.T) {
	tests := []string{
		`{`,
		`{"cniVersion":"0.4.0","name":"pods","type":"netkit-cni","ipam":{"type":"host-local"}}`,
		`{"cniVersion":"1.1.0","name":"pods","type":"bridge","ipam":{"type":"host-local"}}`,
		`{"cniVersion":"1.1.0","name":"pods","type":"netkit-cni","vrfName":"interface-name-is-too-long","ipam":{"type":"host-local"}}`,
		`{"cniVersion":"1.1.0","name":"pods","type":"netkit-cni","ipam":{}}`,
	}
	for _, input := range tests {
		if _, err := Parse([]byte(input)); err == nil {
			t.Errorf("Parse(%s) succeeded, want error", input)
		}
	}
}

func TestIPv4HostPrefix(t *testing.T) {
	got, err := IPv4HostPrefix(net.ParseIP("10.244.1.7"))
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "10.244.1.7/32" {
		t.Fatalf("got %s", got)
	}
	if _, err := IPv4HostPrefix(net.ParseIP("2001:db8::1")); err == nil {
		t.Fatal("IPv6 conversion succeeded")
	}
}
