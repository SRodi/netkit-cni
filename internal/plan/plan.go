package plan

import (
	"fmt"
	"net"
	"sort"
)

const (
	RouteProtocol    = 98
	PodRouteProtocol = 99
)

type Node struct {
	Name       string
	PodCIDR    *net.IPNet
	InternalIP net.IP
}

type Route struct {
	Destination *net.IPNet
	Gateway     net.IP
	Protocol    int
}

type NodePlan struct {
	LocalPodCIDR *net.IPNet
	Main         []Route
}

func Build(localName string, nodes []Node) (NodePlan, error) {
	if localName == "" {
		return NodePlan{}, fmt.Errorf("local node name is required")
	}

	var out NodePlan
	foundLocal := false
	for _, node := range nodes {
		if node.Name == "" || node.PodCIDR == nil || node.PodCIDR.IP.To4() == nil || node.InternalIP.To4() == nil {
			return NodePlan{}, fmt.Errorf("node %q lacks a valid IPv4 PodCIDR or InternalIP", node.Name)
		}
		route := Route{Destination: cloneCIDR(node.PodCIDR), Protocol: RouteProtocol}
		if node.Name == localName {
			foundLocal = true
			out.LocalPodCIDR = cloneCIDR(node.PodCIDR)
			continue
		}
		route.Gateway = append(net.IP(nil), node.InternalIP.To4()...)
		out.Main = append(out.Main, route)
	}
	if !foundLocal {
		return NodePlan{}, fmt.Errorf("local node %q was not returned by the API", localName)
	}
	sort.Slice(out.Main, func(i, j int) bool {
		return out.Main[i].Destination.String() < out.Main[j].Destination.String()
	})
	return out, nil
}

func cloneCIDR(in *net.IPNet) *net.IPNet {
	return &net.IPNet{IP: append(net.IP(nil), in.IP...), Mask: append(net.IPMask(nil), in.Mask...)}
}
