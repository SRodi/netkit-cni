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
	Device      string
	Table       int
	Protocol    int
}

type NodePlan struct {
	LocalPodCIDR *net.IPNet
	Main         []Route
	VRF          []Route
}

func Build(localName string, nodes []Node, clusterCIDR *net.IPNet, transitMain, transitVRF string, vrfTable int, mainIP, vrfIP net.IP) (NodePlan, error) {
	if localName == "" || clusterCIDR == nil || clusterCIDR.IP.To4() == nil {
		return NodePlan{}, fmt.Errorf("local node name and IPv4 cluster CIDR are required")
	}
	if mainIP.To4() == nil || vrfIP.To4() == nil {
		return NodePlan{}, fmt.Errorf("IPv4 transit addresses are required")
	}
	if transitMain == "" || transitVRF == "" || vrfTable <= 0 {
		return NodePlan{}, fmt.Errorf("transit interfaces and positive VRF table are required")
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
			route.Device = transitMain
		} else {
			route.Gateway = append(net.IP(nil), node.InternalIP.To4()...)
		}
		out.Main = append(out.Main, route)
	}
	if !foundLocal {
		return NodePlan{}, fmt.Errorf("local node %q was not returned by the API", localName)
	}
	sort.Slice(out.Main, func(i, j int) bool {
		return out.Main[i].Destination.String() < out.Main[j].Destination.String()
	})
	out.VRF = []Route{{
		Destination: cloneCIDR(clusterCIDR),
		Device:      transitVRF,
		Table:       vrfTable,
		Protocol:    RouteProtocol,
	}, {
		Destination: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		Device:      transitVRF,
		Table:       vrfTable,
		Protocol:    RouteProtocol,
	}}
	return out, nil
}

func ClusterCIDR(nodes []Node) (*net.IPNet, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no nodes found")
	}
	first := uint32(^uint32(0))
	last := uint32(0)
	for _, node := range nodes {
		if node.PodCIDR == nil || node.PodCIDR.IP.To4() == nil {
			return nil, fmt.Errorf("node %q has no IPv4 PodCIDR", node.Name)
		}
		ones, bits := node.PodCIDR.Mask.Size()
		if bits != 32 || ones < 0 {
			return nil, fmt.Errorf("node %q has invalid PodCIDR %q", node.Name, node.PodCIDR)
		}
		base := ipToUint32(node.PodCIDR.IP.Mask(node.PodCIDR.Mask))
		end := base | ^maskToUint32(node.PodCIDR.Mask)
		if base < first {
			first = base
		}
		if end > last {
			last = end
		}
	}
	diff := first ^ last
	prefix := 32
	for diff != 0 {
		prefix--
		diff >>= 1
	}
	mask := net.CIDRMask(prefix, 32)
	return &net.IPNet{IP: uint32ToIP(first).Mask(mask), Mask: mask}, nil
}

func cloneCIDR(in *net.IPNet) *net.IPNet {
	return &net.IPNet{IP: append(net.IP(nil), in.IP...), Mask: append(net.IPMask(nil), in.Mask...)}
}

func ipToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func maskToUint32(mask net.IPMask) uint32 {
	return uint32(mask[0])<<24 | uint32(mask[1])<<16 | uint32(mask[2])<<8 | uint32(mask[3])
}

func uint32ToIP(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
