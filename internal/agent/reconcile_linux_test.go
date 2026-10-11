//go:build linux

package agent

import (
	"errors"
	"net"
	"reflect"
	"testing"

	"github.com/srodi/netkit-cni/internal/plan"
)

type fakeIPTables struct {
	rules       []string
	listErr     error
	clears      int
	appends     [][]string
	uniqueRules [][]string
}

func (f *fakeIPTables) Append(table, chain string, rulespec ...string) error {
	f.appends = append(f.appends, append([]string{table, chain}, rulespec...))
	return nil
}

func (f *fakeIPTables) AppendUnique(table, chain string, rulespec ...string) error {
	f.uniqueRules = append(f.uniqueRules, append([]string{table, chain}, rulespec...))
	return nil
}

func (f *fakeIPTables) ClearChain(_, _ string) error {
	f.clears++
	return nil
}

func (f *fakeIPTables) List(_, _ string) ([]string, error) {
	return f.rules, f.listErr
}

func TestReconcileMasqueradeCreatesSortedRules(t *testing.T) {
	ipt := &fakeIPTables{listErr: errors.New("chain does not exist")}
	nodes := []plan.Node{
		{Name: "node-b", PodCIDR: testCIDR(t, "10.244.1.0/24")},
		{Name: "node-a", PodCIDR: testCIDR(t, "10.244.0.0/24")},
	}

	if err := reconcileMasquerade(ipt, testCIDR(t, "10.244.0.0/24"), nodes); err != nil {
		t.Fatal(err)
	}

	if ipt.clears != 1 {
		t.Fatalf("clear calls = %d, want 1", ipt.clears)
	}
	wantRules := [][]string{
		{"nat", masqueradeChain, "-s", "10.244.0.0/24", "-d", "10.244.0.0/24", "-j", "RETURN"},
		{"nat", masqueradeChain, "-s", "10.244.0.0/24", "-d", "10.244.1.0/24", "-j", "RETURN"},
		{"nat", masqueradeChain, "-s", "10.244.0.0/24", "-j", "MASQUERADE"},
	}
	if !reflect.DeepEqual(ipt.appends, wantRules) {
		t.Fatalf("rules = %#v, want %#v", ipt.appends, wantRules)
	}
	wantHook := [][]string{{"nat", masqueradeParentChain, "-j", masqueradeChain}}
	if !reflect.DeepEqual(ipt.uniqueRules, wantHook) {
		t.Fatalf("hooks = %#v, want %#v", ipt.uniqueRules, wantHook)
	}
}

func TestReconcileMasqueradeLeavesCurrentRules(t *testing.T) {
	ipt := &fakeIPTables{rules: []string{
		"-N " + masqueradeChain,
		"-A " + masqueradeChain + " -s 10.244.0.0/24 -d 10.244.0.0/24 -j RETURN",
		"-A " + masqueradeChain + " -s 10.244.0.0/24 -j MASQUERADE",
	}}
	nodes := []plan.Node{{Name: "node-a", PodCIDR: testCIDR(t, "10.244.0.0/24")}}

	if err := reconcileMasquerade(ipt, testCIDR(t, "10.244.0.0/24"), nodes); err != nil {
		t.Fatal(err)
	}

	if ipt.clears != 0 || len(ipt.appends) != 0 {
		t.Fatalf("current chain was rewritten: clears=%d appends=%#v", ipt.clears, ipt.appends)
	}
	if len(ipt.uniqueRules) != 1 {
		t.Fatalf("hook checks = %d, want 1", len(ipt.uniqueRules))
	}
}

func testCIDR(t *testing.T, value string) *net.IPNet {
	t.Helper()
	ip, network, err := net.ParseCIDR(value)
	if err != nil {
		t.Fatal(err)
	}
	network.IP = ip
	return network
}
