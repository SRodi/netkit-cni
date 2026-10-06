//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ipam"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/srodi/netkit-cni/internal/config"
	"github.com/srodi/netkit-cni/internal/naming"
	"github.com/srodi/netkit-cni/internal/plan"
	"github.com/vishvananda/netlink"
)

func main() {
	skel.PluginMainFuncs(skel.CNIFuncs{
		Add:   cmdAdd,
		Del:   cmdDel,
		Check: cmdCheck,
	}, version.PluginSupports("1.0.0", "1.1.0"), "netkit-cni")
}

func cmdAdd(args *skel.CmdArgs) (retErr error) {
	conf, err := config.Parse(args.StdinData)
	if err != nil {
		return err
	}
	if args.ContainerID == "" || args.Netns == "" || args.IfName == "" {
		return fmt.Errorf("CNI_CONTAINERID, CNI_NETNS, and CNI_IFNAME are required for ADD")
	}
	hostName, err := naming.HostInterface(args.ContainerID)
	if err != nil {
		return err
	}
	targetNS, err := ns.GetNS(args.Netns)
	if err != nil {
		return fmt.Errorf("open target network namespace %q: %w", args.Netns, err)
	}
	defer targetNS.Close()

	ipamResult, err := ipam.ExecAdd(conf.IPAM.Type, args.StdinData)
	if err != nil {
		return fmt.Errorf("allocate address with %s: %w", conf.IPAM.Type, err)
	}
	success := false
	defer func() {
		if success {
			return
		}
		cleanupErr := deleteHostLink(hostName)
		ipamErr := ipam.ExecDel(conf.IPAM.Type, args.StdinData)
		retErr = errors.Join(retErr, cleanupErr, wrapIfError("roll back IPAM allocation", ipamErr))
	}()

	result, podAddress, err := resultForPod(ipamResult, hostName, args.IfName, args.Netns)
	if err != nil {
		return err
	}
	vrf, err := requireVRF(conf.VRFName, conf.VRFTable)
	if err != nil {
		return err
	}

	netkit := &netlink.Netkit{
		LinkAttrs:  netlink.LinkAttrs{Name: hostName},
		Mode:       netlink.NETKIT_MODE_L3,
		Policy:     netlink.NETKIT_POLICY_FORWARD,
		PeerPolicy: netlink.NETKIT_POLICY_FORWARD,
	}
	netkit.SetPeerAttrs(&netlink.LinkAttrs{
		Name:      args.IfName,
		Namespace: netlink.NsFd(targetNS.Fd()),
	})
	if err := netlink.LinkAdd(netkit); err != nil {
		return fmt.Errorf("create L3 netkit pair %s/%s: %w", hostName, args.IfName, err)
	}

	host, err := netlink.LinkByName(hostName)
	if err != nil {
		return fmt.Errorf("look up new host netkit %s: %w", hostName, err)
	}
	if err := netlink.LinkSetMaster(host, vrf); err != nil {
		return fmt.Errorf("enslave %s to VRF %s: %w", hostName, conf.VRFName, err)
	}
	if err := netlink.LinkSetUp(host); err != nil {
		return fmt.Errorf("set host netkit %s up: %w", hostName, err)
	}
	hostRoute := &netlink.Route{
		LinkIndex: host.Attrs().Index,
		Dst:       podAddress,
		Scope:     netlink.SCOPE_LINK,
		Table:     conf.VRFTable,
		Protocol:  netlink.RouteProtocol(plan.PodRouteProtocol),
	}
	if err := netlink.RouteReplace(hostRoute); err != nil {
		return fmt.Errorf("install VRF route for %s: %w", podAddress, err)
	}

	if err := targetNS.Do(func(_ ns.NetNS) error {
		peer, err := netlink.LinkByName(args.IfName)
		if err != nil {
			return fmt.Errorf("look up Pod netkit %s: %w", args.IfName, err)
		}
		if err := netlink.AddrReplace(peer, &netlink.Addr{IPNet: podAddress}); err != nil {
			return fmt.Errorf("assign %s to %s: %w", podAddress, args.IfName, err)
		}
		if err := netlink.LinkSetUp(peer); err != nil {
			return fmt.Errorf("set Pod netkit %s up: %w", args.IfName, err)
		}
		defaultRoute := &netlink.Route{
			LinkIndex: peer.Attrs().Index,
			Dst:       &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
			Scope:     netlink.SCOPE_LINK,
		}
		if err := netlink.RouteReplace(defaultRoute); err != nil {
			return fmt.Errorf("install default device route on %s: %w", args.IfName, err)
		}
		return nil
	}); err != nil {
		return err
	}

	if err := types.PrintResult(result, conf.CNIVersion); err != nil {
		return fmt.Errorf("print CNI result: %w", err)
	}
	success = true
	return nil
}

func cmdDel(args *skel.CmdArgs) error {
	conf, err := config.Parse(args.StdinData)
	if err != nil {
		return err
	}
	var linkErr error
	if args.ContainerID == "" {
		linkErr = fmt.Errorf("CNI_CONTAINERID is required for DEL")
	} else {
		hostName, nameErr := naming.HostInterface(args.ContainerID)
		if nameErr != nil {
			linkErr = nameErr
		} else {
			linkErr = deleteHostLink(hostName)
		}
	}
	ipamErr := ipam.ExecDel(conf.IPAM.Type, args.StdinData)
	return errors.Join(linkErr, wrapIfError("release IPAM allocation", ipamErr))
}

func cmdCheck(args *skel.CmdArgs) error {
	conf, err := config.Parse(args.StdinData)
	if err != nil {
		return err
	}
	if args.ContainerID == "" || args.Netns == "" || args.IfName == "" {
		return fmt.Errorf("CNI_CONTAINERID, CNI_NETNS, and CNI_IFNAME are required for CHECK")
	}
	if conf.RawPrevResult == nil {
		return fmt.Errorf("prevResult is required for CHECK")
	}
	if err := version.ParsePrevResult(&conf.NetConf); err != nil {
		return fmt.Errorf("parse prevResult: %w", err)
	}
	result, err := current.NewResultFromResult(conf.PrevResult)
	if err != nil {
		return fmt.Errorf("convert prevResult: %w", err)
	}
	expected, err := podIPv4(result)
	if err != nil {
		return fmt.Errorf("validate prevResult: %w", err)
	}
	if err := ipam.ExecCheck(conf.IPAM.Type, args.StdinData); err != nil {
		return fmt.Errorf("check IPAM allocation: %w", err)
	}

	hostName, err := naming.HostInterface(args.ContainerID)
	if err != nil {
		return err
	}
	host, err := netlink.LinkByName(hostName)
	if err != nil {
		return fmt.Errorf("host netkit %s is missing: %w", hostName, err)
	}
	hostNetkit, ok := host.(*netlink.Netkit)
	if !ok || !hostNetkit.IsPrimary() || hostNetkit.Mode != netlink.NETKIT_MODE_L3 ||
		hostNetkit.Policy != netlink.NETKIT_POLICY_FORWARD || hostNetkit.PeerPolicy != netlink.NETKIT_POLICY_FORWARD {
		return fmt.Errorf("host interface %s is not a primary L3 netkit with FORWARD policy on both ends", hostName)
	}
	vrf, err := requireVRF(conf.VRFName, conf.VRFTable)
	if err != nil {
		return err
	}
	if host.Attrs().MasterIndex != vrf.Attrs().Index {
		return fmt.Errorf("host netkit %s is not enslaved to VRF %s", hostName, conf.VRFName)
	}
	if host.Attrs().Flags&net.FlagUp == 0 {
		return fmt.Errorf("host netkit %s is down", hostName)
	}
	hostPrefix, _ := config.IPv4HostPrefix(expected.Address.IP)
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{
		Dst:   hostPrefix,
		Table: conf.VRFTable,
	}, netlink.RT_FILTER_DST|netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("list VRF route for %s: %w", hostPrefix, err)
	}
	if len(routes) != 1 || routes[0].LinkIndex != host.Attrs().Index {
		return fmt.Errorf("expected one VRF table %d route for %s through %s", conf.VRFTable, hostPrefix, hostName)
	}

	targetNS, err := ns.GetNS(args.Netns)
	if err != nil {
		return fmt.Errorf("open target network namespace %q: %w", args.Netns, err)
	}
	defer targetNS.Close()
	return targetNS.Do(func(_ ns.NetNS) error {
		peer, err := netlink.LinkByName(args.IfName)
		if err != nil {
			return fmt.Errorf("Pod netkit %s is missing: %w", args.IfName, err)
		}
		peerNetkit, ok := peer.(*netlink.Netkit)
		if !ok || peerNetkit.IsPrimary() || peerNetkit.Mode != netlink.NETKIT_MODE_L3 {
			return fmt.Errorf("Pod interface %s is not the peer of an L3 netkit", args.IfName)
		}
		if peer.Attrs().Flags&net.FlagUp == 0 {
			return fmt.Errorf("Pod netkit %s is down", args.IfName)
		}
		addrs, err := netlink.AddrList(peer, netlink.FAMILY_V4)
		if err != nil {
			return fmt.Errorf("list addresses on %s: %w", args.IfName, err)
		}
		if len(addrs) != 1 || addrs[0].IPNet.String() != hostPrefix.String() {
			return fmt.Errorf("Pod netkit %s has addresses %v, want only %s", args.IfName, addrs, hostPrefix)
		}
		defaults, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{
			LinkIndex: peer.Attrs().Index,
			Dst:       &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		}, netlink.RT_FILTER_OIF|netlink.RT_FILTER_DST)
		if err != nil {
			return fmt.Errorf("list default route on %s: %w", args.IfName, err)
		}
		if len(defaults) != 1 || defaults[0].Gw != nil {
			return fmt.Errorf("Pod netkit %s must have exactly one gateway-free default route", args.IfName)
		}
		return nil
	})
}

func resultForPod(raw types.Result, hostName, ifName, sandbox string) (*current.Result, *net.IPNet, error) {
	result, err := current.NewResultFromResult(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("convert IPAM result: %w", err)
	}
	ipConfig, err := podIPv4(result)
	if err != nil {
		return nil, nil, fmt.Errorf("validate IPAM result: %w", err)
	}
	hostPrefix, err := config.IPv4HostPrefix(ipConfig.Address.IP)
	if err != nil {
		return nil, nil, err
	}
	result.Interfaces = []*current.Interface{
		{Name: hostName},
		{Name: ifName, Sandbox: sandbox},
	}
	ipConfig.Address = *hostPrefix
	ipConfig.Gateway = nil
	ipConfig.Interface = current.Int(1)
	result.IPs = []*current.IPConfig{ipConfig}
	result.Routes = []*types.Route{{
		Dst: net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
	}}
	return result, hostPrefix, nil
}

func requireVRF(name string, table int) (netlink.Link, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("required VRF %s is missing: %w", name, err)
	}
	vrf, ok := link.(*netlink.Vrf)
	if !ok || int(vrf.Table) != table {
		return nil, fmt.Errorf("link %s is not VRF table %d", name, table)
	}
	return link, nil
}

func deleteHostLink(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		var notFound netlink.LinkNotFoundError
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("look up host netkit %s: %w", name, err)
	}
	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("delete host netkit %s: %w", name, err)
	}
	return nil
}

func wrapIfError(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}
