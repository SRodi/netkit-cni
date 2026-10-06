package agent

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNodesFromAPI(t *testing.T) {
	got, err := NodesFromAPI([]corev1.Node{{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
		Spec:       corev1.NodeSpec{PodCIDR: "10.244.1.0/24"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeHostName, Address: "node-a"},
			{Type: corev1.NodeInternalIP, Address: "172.18.0.2"},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PodCIDR.String() != "10.244.1.0/24" || got[0].InternalIP.String() != "172.18.0.2" {
		t.Fatalf("unexpected nodes: %#v", got)
	}
}

func TestNodesFromAPIRejectsIPv6(t *testing.T) {
	_, err := NodesFromAPI([]corev1.Node{{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
		Spec:       corev1.NodeSpec{PodCIDR: "2001:db8::/64"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: "2001:db8::1"},
		}},
	}})
	if err == nil {
		t.Fatal("IPv6 node succeeded")
	}
}
