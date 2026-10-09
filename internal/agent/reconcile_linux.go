//go:build linux

package agent

import (
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/srodi/netkit-cni/internal/plan"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	PodRulePriority = 500
	legacyMainName  = "nk-main"
	legacyVRFName   = "nk-vrf-x"
)

var VRFAddress = mustCIDR("169.254.100.1/32")

type Reconciler struct {
	VRFName  string
	VRFTable int
}

func (r Reconciler) Reconcile(localName string, nodes []plan.Node) error {
	if r.VRFName == "" || r.VRFTable <= 0 {
		return fmt.Errorf("VRF name and positive table are required")
	}
	if err := enableForwarding(); err != nil {
		return err
	}
	vrf, err := ensureVRF(r.VRFName, r.VRFTable)
	if err != nil {
		return err
	}
	if err := netlink.AddrReplace(vrf, &netlink.Addr{IPNet: VRFAddress}); err != nil {
		return fmt.Errorf("assign node-local address to VRF %s: %w", r.VRFName, err)
	}
	if err := configureForwardingInterface(r.VRFName); err != nil {
		return err
	}
	routePlan, err := plan.Build(localName, nodes)
	if err != nil {
		return err
	}
	if err := reconcileRoutes(routePlan.Main, unix.RT_TABLE_MAIN); err != nil {
		return fmt.Errorf("reconcile main routes: %w", err)
	}
	if err := reconcileRoutes(nil, r.VRFTable); err != nil {
		return fmt.Errorf("remove obsolete VRF routes: %w", err)
	}
	if err := reconcilePodRule(routePlan.LocalPodCIDR, r.VRFTable); err != nil {
		return fmt.Errorf("reconcile local PodCIDR policy rule: %w", err)
	}
	if err := removeLegacyTransit(); err != nil {
		return err
	}
	return nil
}

func ensureVRF(name string, table int) (netlink.Link, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		var notFound netlink.LinkNotFoundError
		if !errors.As(err, &notFound) {
			return nil, fmt.Errorf("look up VRF %s: %w", name, err)
		}
		vrf := &netlink.Vrf{
			LinkAttrs: netlink.LinkAttrs{Name: name},
			Table:     uint32(table),
		}
		if err := netlink.LinkAdd(vrf); err != nil {
			return nil, fmt.Errorf("create VRF %s table %d: %w", name, table, err)
		}
		link = vrf
	}
	vrf, ok := link.(*netlink.Vrf)
	if !ok || int(vrf.Table) != table {
		return nil, fmt.Errorf("existing link %s is not VRF table %d", name, table)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return nil, fmt.Errorf("set VRF %s up: %w", name, err)
	}
	return link, nil
}

func reconcileRoutes(desired []plan.Route, table int) error {
	wanted := make(map[string]netlink.Route, len(desired))
	for _, route := range desired {
		nlRoute := netlink.Route{
			Dst:      route.Destination,
			Gw:       route.Gateway,
			Table:    table,
			Protocol: netlink.RouteProtocol(route.Protocol),
		}
		if route.Gateway != nil {
			resolved, err := netlink.RouteGet(route.Gateway)
			if err != nil || len(resolved) == 0 || resolved[0].LinkIndex == 0 {
				return fmt.Errorf("resolve route to gateway %s: %w", route.Gateway, err)
			}
			nlRoute.LinkIndex = resolved[0].LinkIndex
		}
		wanted[routeKey(nlRoute)] = nlRoute
	}

	existing, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{
		Table:    table,
		Protocol: netlink.RouteProtocol(plan.RouteProtocol),
	}, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL)
	if err != nil {
		return fmt.Errorf("list protocol %d routes in table %d: %w", plan.RouteProtocol, table, err)
	}
	for _, route := range existing {
		if _, ok := wanted[routeKey(route)]; ok {
			continue
		}
		if err := netlink.RouteDel(&route); err != nil {
			return fmt.Errorf("delete stale route %s: %w", route.Dst, err)
		}
	}
	for _, route := range wanted {
		copy := route
		if err := netlink.RouteReplace(&copy); err != nil {
			return fmt.Errorf("replace route %s in table %d: %w", route.Dst, table, err)
		}
	}
	return nil
}

func removeLegacyTransit() error {
	for _, name := range []string{legacyMainName, legacyVRFName} {
		link, err := netlink.LinkByName(name)
		if err != nil {
			var notFound netlink.LinkNotFoundError
			if errors.As(err, &notFound) {
				continue
			}
			return fmt.Errorf("look up obsolete transit link %s: %w", name, err)
		}
		if _, ok := link.(*netlink.Netkit); !ok {
			return fmt.Errorf("obsolete transit name %s is occupied by a non-netkit link", name)
		}
		if err := netlink.LinkDel(link); err != nil {
			return fmt.Errorf("delete obsolete transit link %s: %w", name, err)
		}
	}
	return nil
}

func routeKey(route netlink.Route) string {
	return fmt.Sprintf("%s|%s|%d|%d", route.Dst, route.Gw, route.LinkIndex, route.Table)
}

func reconcilePodRule(localPodCIDR *net.IPNet, table int) error {
	if localPodCIDR == nil {
		return fmt.Errorf("local PodCIDR is required")
	}
	existing, err := netlink.RuleListFiltered(netlink.FAMILY_V4, &netlink.Rule{
		Priority: PodRulePriority,
	}, netlink.RT_FILTER_PRIORITY)
	if err != nil {
		return fmt.Errorf("list priority %d rules: %w", PodRulePriority, err)
	}

	found := false
	for i := range existing {
		rule := &existing[i]
		if rule.Dst != nil && rule.Dst.String() == localPodCIDR.String() && rule.Table == table {
			found = true
			continue
		}
		if rule.Protocol != uint8(plan.RouteProtocol) {
			return fmt.Errorf("priority %d is occupied by an unmanaged rule: %s", PodRulePriority, rule)
		}
		if err := netlink.RuleDel(rule); err != nil {
			return fmt.Errorf("delete stale rule %s: %w", rule, err)
		}
	}
	if found {
		return nil
	}

	rule := netlink.NewRule()
	rule.Family = netlink.FAMILY_V4
	rule.Priority = PodRulePriority
	rule.Table = table
	rule.Dst = localPodCIDR
	rule.Protocol = uint8(plan.RouteProtocol)
	if err := netlink.RuleAdd(rule); err != nil {
		return fmt.Errorf("add priority %d rule to %s lookup table %d: %w", PodRulePriority, localPodCIDR, table, err)
	}
	return nil
}

func enableForwarding() error {
	settings := map[string]string{
		"/proc/sys/net/ipv4/ip_forward":              "1\n",
		"/proc/sys/net/ipv4/tcp_l3mdev_accept":       "1\n",
		"/proc/sys/net/ipv4/udp_l3mdev_accept":       "1\n",
		"/proc/sys/net/ipv4/conf/all/forwarding":     "1\n",
		"/proc/sys/net/ipv4/conf/default/forwarding": "1\n",
		"/proc/sys/net/ipv4/conf/all/rp_filter":      "0\n",
		"/proc/sys/net/ipv4/conf/default/rp_filter":  "0\n",
	}
	for path, value := range settings {
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			return fmt.Errorf("write sysctl %s: %w", path, err)
		}
	}
	return nil
}

func configureForwardingInterface(name string) error {
	for setting, value := range map[string]string{"forwarding": "1\n", "rp_filter": "0\n"} {
		path := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/%s", name, setting)
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			return fmt.Errorf("write sysctl %s: %w", path, err)
		}
	}
	return nil
}

func mustCIDR(value string) *net.IPNet {
	ip, network, err := net.ParseCIDR(value)
	if err != nil {
		panic(err)
	}
	network.IP = ip
	return network
}
