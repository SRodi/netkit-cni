//go:build linux

package agent

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/srodi/netkit-cni/internal/plan"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	TransitMainName = "nk-main"
	TransitVRFName  = "nk-vrf-x"
	PodRulePriority = 500
)

var (
	TransitMainAddress = mustCIDR("169.254.100.1/30")
	TransitVRFAddress  = mustCIDR("169.254.100.2/30")
)

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
	mainLink, vrfLink, err := ensureTransit(vrf)
	if err != nil {
		return err
	}
	if err := configureForwardingInterfaces(r.VRFName, TransitMainName, TransitVRFName); err != nil {
		return err
	}
	clusterCIDR, err := plan.ClusterCIDR(nodes)
	if err != nil {
		return err
	}
	routePlan, err := plan.Build(localName, nodes, clusterCIDR, TransitMainName, TransitVRFName, r.VRFTable, TransitMainAddress.IP, TransitVRFAddress.IP)
	if err != nil {
		return err
	}
	links := map[string]netlink.Link{
		TransitMainName: mainLink,
		TransitVRFName:  vrfLink,
	}
	if err := reconcileRoutes(routePlan.Main, unix.RT_TABLE_MAIN, links); err != nil {
		return fmt.Errorf("reconcile main routes: %w", err)
	}
	if err := reconcileRoutes(routePlan.VRF, r.VRFTable, links); err != nil {
		return fmt.Errorf("reconcile VRF routes: %w", err)
	}
	if err := reconcilePodRule(routePlan.LocalPodCIDR, r.VRFTable); err != nil {
		return fmt.Errorf("reconcile local PodCIDR policy rule: %w", err)
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

func ensureTransit(vrf netlink.Link) (netlink.Link, netlink.Link, error) {
	mainLink, mainErr := netlink.LinkByName(TransitMainName)
	vrfLink, vrfErr := netlink.LinkByName(TransitVRFName)
	if mainErr != nil || vrfErr != nil {
		var mainMissing, vrfMissing netlink.LinkNotFoundError
		if !(errors.As(mainErr, &mainMissing) && errors.As(vrfErr, &vrfMissing)) {
			return nil, nil, fmt.Errorf("transit pair is incomplete or inaccessible: %s: %v, %s: %v", TransitMainName, mainErr, TransitVRFName, vrfErr)
		}
		netkit := &netlink.Netkit{
			LinkAttrs:  netlink.LinkAttrs{Name: TransitMainName},
			Mode:       netlink.NETKIT_MODE_L3,
			Policy:     netlink.NETKIT_POLICY_FORWARD,
			PeerPolicy: netlink.NETKIT_POLICY_FORWARD,
		}
		netkit.SetPeerAttrs(&netlink.LinkAttrs{Name: TransitVRFName})
		if err := netlink.LinkAdd(netkit); err != nil {
			return nil, nil, fmt.Errorf("create L3 netkit transit pair: %w", err)
		}
		mainLink, _ = netlink.LinkByName(TransitMainName)
		vrfLink, _ = netlink.LinkByName(TransitVRFName)
	}
	mainNetkit, mainOK := mainLink.(*netlink.Netkit)
	vrfNetkit, vrfOK := vrfLink.(*netlink.Netkit)
	if !mainOK || !vrfOK || !mainNetkit.IsPrimary() || vrfNetkit.IsPrimary() ||
		mainNetkit.Mode != netlink.NETKIT_MODE_L3 ||
		vrfNetkit.Mode != netlink.NETKIT_MODE_L3 ||
		mainNetkit.Policy != netlink.NETKIT_POLICY_FORWARD ||
		mainNetkit.PeerPolicy != netlink.NETKIT_POLICY_FORWARD ||
		vrfNetkit.Policy != netlink.NETKIT_POLICY_FORWARD ||
		vrfNetkit.PeerPolicy != netlink.NETKIT_POLICY_FORWARD ||
		mainLink.Attrs().ParentIndex != vrfLink.Attrs().Index ||
		vrfLink.Attrs().ParentIndex != mainLink.Attrs().Index {
		return nil, nil, fmt.Errorf("existing transit links are not an L3 netkit pair with FORWARD policy")
	}
	if vrfLink.Attrs().MasterIndex != 0 && vrfLink.Attrs().MasterIndex != vrf.Attrs().Index {
		return nil, nil, fmt.Errorf("%s is enslaved to an unexpected master index %d", TransitVRFName, vrfLink.Attrs().MasterIndex)
	}
	if err := netlink.LinkSetMaster(vrfLink, vrf); err != nil {
		return nil, nil, fmt.Errorf("enslave %s to %s: %w", TransitVRFName, vrf.Attrs().Name, err)
	}
	if err := netlink.AddrReplace(mainLink, &netlink.Addr{IPNet: TransitMainAddress}); err != nil {
		return nil, nil, fmt.Errorf("assign transit address to %s: %w", TransitMainName, err)
	}
	if err := netlink.AddrReplace(vrfLink, &netlink.Addr{IPNet: TransitVRFAddress}); err != nil {
		return nil, nil, fmt.Errorf("assign transit address to %s: %w", TransitVRFName, err)
	}
	if err := netlink.LinkSetUp(mainLink); err != nil {
		return nil, nil, fmt.Errorf("set %s up: %w", TransitMainName, err)
	}
	if err := netlink.LinkSetUp(vrfLink); err != nil {
		return nil, nil, fmt.Errorf("set %s up: %w", TransitVRFName, err)
	}
	return mainLink, vrfLink, nil
}

func reconcileRoutes(desired []plan.Route, table int, links map[string]netlink.Link) error {
	wanted := make(map[string]netlink.Route, len(desired))
	for _, route := range desired {
		nlRoute := netlink.Route{
			Dst:      route.Destination,
			Gw:       route.Gateway,
			Table:    table,
			Protocol: netlink.RouteProtocol(route.Protocol),
		}
		if route.Device != "" {
			link, ok := links[route.Device]
			if !ok {
				return fmt.Errorf("route %s references unknown device %s", route.Destination, route.Device)
			}
			nlRoute.LinkIndex = link.Attrs().Index
			nlRoute.Scope = netlink.SCOPE_LINK
		} else if route.Gateway != nil {
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

func configureForwardingInterfaces(names ...string) error {
	for _, name := range names {
		for setting, value := range map[string]string{"forwarding": "1\n", "rp_filter": "0\n"} {
			path := filepath.Join("/proc/sys/net/ipv4/conf", name, setting)
			if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
				return fmt.Errorf("write sysctl %s: %w", path, err)
			}
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
