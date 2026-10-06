//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/srodi/netkit-cni/internal/agent"
	"github.com/srodi/netkit-cni/internal/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	var (
		interval   = flag.Duration("interval", 10*time.Second, "reconciliation interval")
		configPath = flag.String("cni-config", "/host/etc/cni/net.d/10-netkit-cni.conf", "host CNI configuration path")
		vrfName    = flag.String("vrf-name", config.DefaultVRFName, "node VRF name")
		vrfTable   = flag.Int("vrf-table", config.DefaultVRFTable, "node VRF routing table")
	)
	flag.Parse()
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Fatal("NODE_NAME is required")
	}
	if *interval <= 0 {
		log.Fatal("-interval must be positive")
	}
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("build in-cluster Kubernetes config: %v", err)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("create Kubernetes client: %v", err)
	}
	reconciler := agent.Reconciler{VRFName: *vrfName, VRFTable: *vrfTable}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for {
		if err := reconcile(ctx, client, reconciler, nodeName, *configPath); err != nil {
			log.Printf("reconciliation failed: %v", err)
		} else {
			log.Printf("reconciled node %s", nodeName)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(*interval):
		}
	}
}

func reconcile(ctx context.Context, client kubernetes.Interface, reconciler agent.Reconciler, nodeName, configPath string) error {
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	list, err := client.CoreV1().Nodes().List(requestCtx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list Kubernetes Nodes: %w", err)
	}
	nodes, err := agent.NodesFromAPI(list.Items)
	if err != nil {
		return err
	}
	if err := reconciler.Reconcile(nodeName, nodes); err != nil {
		return err
	}
	for _, node := range nodes {
		if node.Name == nodeName {
			if err := agent.WriteCNIConfig(configPath, node.PodCIDR.String(), reconciler.VRFName, reconciler.VRFTable); err != nil {
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("local node %s not found", nodeName)
}
