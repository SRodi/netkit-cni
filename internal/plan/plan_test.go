package plan

import (
	"net"
	"testing"
)

func cidr(t *testing.T, value string) *net.IPNet {
	t.Helper()
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		t.Fatal(err)
	}
	return network
}

func TestBuild(t *testing.T) {
	nodes := []Node{
		{Name: "node-b", PodCIDR: cidr(t, "10.244.1.0/24"), InternalIP: net.ParseIP("172.18.0.3")},
		{Name: "node-a", PodCIDR: cidr(t, "10.244.0.0/24"), InternalIP: net.ParseIP("172.18.0.2")},
	}
	got, err := Build("node-a", nodes)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Main) != 1 || got.Main[0].Destination.String() != "10.244.1.0/24" {
		t.Fatalf("unexpected main routes: %#v", got.Main)
	}
	if got.LocalPodCIDR.String() != "10.244.0.0/24" {
		t.Fatalf("unexpected local PodCIDR: %s", got.LocalPodCIDR)
	}
	if got.Main[0].Gateway.String() != "172.18.0.3" {
		t.Fatalf("unexpected remote route: %#v", got.Main[0])
	}
}

func TestBuildRejectsMissingLocalNode(t *testing.T) {
	nodes := []Node{{Name: "other", PodCIDR: cidr(t, "10.244.1.0/24"), InternalIP: net.ParseIP("172.18.0.3")}}
	if _, err := Build("local", nodes); err == nil {
		t.Fatal("Build succeeded")
	}
}
