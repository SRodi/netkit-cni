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

func TestClusterCIDR(t *testing.T) {
	nodes := []Node{
		{Name: "a", PodCIDR: cidr(t, "10.244.0.0/24"), InternalIP: net.ParseIP("172.18.0.2")},
		{Name: "b", PodCIDR: cidr(t, "10.244.1.0/24"), InternalIP: net.ParseIP("172.18.0.3")},
		{Name: "c", PodCIDR: cidr(t, "10.244.2.0/24"), InternalIP: net.ParseIP("172.18.0.4")},
	}
	got, err := ClusterCIDR(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "10.244.0.0/22" {
		t.Fatalf("got %s", got)
	}
}

func TestBuild(t *testing.T) {
	nodes := []Node{
		{Name: "node-b", PodCIDR: cidr(t, "10.244.1.0/24"), InternalIP: net.ParseIP("172.18.0.3")},
		{Name: "node-a", PodCIDR: cidr(t, "10.244.0.0/24"), InternalIP: net.ParseIP("172.18.0.2")},
	}
	got, err := Build("node-a", nodes, cidr(t, "10.244.0.0/16"), "nk-main", "nk-vrf-x", 100, net.ParseIP("169.254.100.1"), net.ParseIP("169.254.100.2"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Main) != 2 || got.Main[0].Destination.String() != "10.244.0.0/24" {
		t.Fatalf("unexpected main routes: %#v", got.Main)
	}
	if got.LocalPodCIDR.String() != "10.244.0.0/24" {
		t.Fatalf("unexpected local PodCIDR: %s", got.LocalPodCIDR)
	}
	if got.Main[0].Gateway != nil || got.Main[0].Device != "nk-main" {
		t.Fatalf("unexpected local route: %#v", got.Main[0])
	}
	if got.Main[1].Gateway.String() != "172.18.0.3" || got.Main[1].Device != "" {
		t.Fatalf("unexpected remote route: %#v", got.Main[1])
	}
	if len(got.VRF) != 2 || got.VRF[0].Destination.String() != "10.244.0.0/16" ||
		got.VRF[1].Destination.String() != "0.0.0.0/0" {
		t.Fatalf("unexpected VRF route: %#v", got.VRF)
	}
	for _, route := range got.VRF {
		if route.Table != 100 || route.Gateway != nil || route.Device != "nk-vrf-x" {
			t.Fatalf("unexpected VRF route: %#v", route)
		}
	}
}

func TestBuildRejectsMissingLocalNode(t *testing.T) {
	nodes := []Node{{Name: "other", PodCIDR: cidr(t, "10.244.1.0/24"), InternalIP: net.ParseIP("172.18.0.3")}}
	if _, err := Build("local", nodes, cidr(t, "10.244.0.0/16"), "a", "b", 100, net.ParseIP("169.254.100.1"), net.ParseIP("169.254.100.2")); err == nil {
		t.Fatal("Build succeeded")
	}
}
