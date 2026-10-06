package agent

import (
	"fmt"
	"net"

	"github.com/srodi/netkit-cni/internal/plan"
	corev1 "k8s.io/api/core/v1"
)

func NodesFromAPI(items []corev1.Node) ([]plan.Node, error) {
	nodes := make([]plan.Node, 0, len(items))
	for _, item := range items {
		if item.Spec.PodCIDR == "" {
			return nil, fmt.Errorf("node %s has no PodCIDR", item.Name)
		}
		ip, podCIDR, err := net.ParseCIDR(item.Spec.PodCIDR)
		if err != nil || ip.To4() == nil {
			return nil, fmt.Errorf("node %s has invalid IPv4 PodCIDR %q", item.Name, item.Spec.PodCIDR)
		}
		podCIDR.IP = ip.Mask(podCIDR.Mask)
		var internalIP net.IP
		for _, address := range item.Status.Addresses {
			if address.Type != corev1.NodeInternalIP {
				continue
			}
			parsed := net.ParseIP(address.Address)
			if parsed == nil || parsed.To4() == nil {
				continue
			}
			if internalIP != nil {
				return nil, fmt.Errorf("node %s has multiple IPv4 InternalIP addresses", item.Name)
			}
			internalIP = parsed.To4()
		}
		if internalIP == nil {
			return nil, fmt.Errorf("node %s has no IPv4 InternalIP", item.Name)
		}
		nodes = append(nodes, plan.Node{Name: item.Name, PodCIDR: podCIDR, InternalIP: internalIP})
	}
	return nodes, nil
}
