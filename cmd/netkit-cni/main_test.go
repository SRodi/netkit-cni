package main

import (
	"strings"
	"testing"

	current "github.com/containernetworking/cni/pkg/types/100"
)

func TestPodIPv4RejectsNullEntry(t *testing.T) {
	t.Parallel()

	_, err := podIPv4(&current.Result{IPs: []*current.IPConfig{nil}})
	if err == nil || !strings.Contains(err.Error(), "null IP entry") {
		t.Fatalf("podIPv4() error = %v, want null IP entry error", err)
	}
}
