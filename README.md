# netkit-cni

`netkit-cni` is a small, educational Kubernetes CNI that connects Pods with
Linux **L3 netkit** devices and places the host ends in a Linux **VRF**. It is
intended to make the routing mechanics easy to inspect. It is not a production
network plugin.

The datapath has:

- no bridge;
- no overlay or encapsulation;
- no NAT or masquerading;
- no per-Pod veth;
- no ARP on a Pod link; and
- no BPF program. Both ends of every netkit pair use the kernel's `FORWARD`
  policy.

Only IPv4 is supported.

## Prerequisites

- A native Linux host or Linux VM with kernel **6.8 or newer** and
  `CONFIG_NETKIT`, `CONFIG_NET_L3_MASTER_DEV`, and `CONFIG_NET_VRF` enabled.
  Netkit entered Linux in 6.7; 6.8 is the baseline documented and supported by
  Cilium and is the baseline used here.
- Root privileges for the standalone integration test.
- Go 1.26 or newer.
- Docker, kind, and kubectl for the Kubernetes test.
- A recent `ip` command for the human-facing inspection and test scripts.
  The CNI and agent themselves use rtnetlink through Go and do not invoke
  `iproute2`.

On a new Ubuntu VM, install the user-space dependencies—including `make`—with:

```sh
sudo ./scripts/install-prerequisites-ubuntu.sh
```

The installer uses the official Docker apt repository and checksum-verified
Go, kind, and kubectl downloads. It installs Docker Engine, Go 1.26.2, kind
0.33.0, kubectl 1.37.0, `git`, `make`, `iproute2`, and supporting tools.
Running `sudo make install-prerequisites` is an equivalent convenience after
`make` is available. Versions can be overridden with `GO_VERSION`,
`KIND_VERSION`, and `KUBECTL_VERSION`. If it adds your user to the `docker`
group, log out and back in before continuing.

The kernel is supplied by the container runtime host, not by
`kindest/node`. On macOS, that means the Docker Desktop VM kernel. A modern
Kubernetes node image cannot add netkit support to a host kernel that lacks it.
On the Linux host or VM that will run Docker, use the project checker:

```sh
make prerequisites
```

The script checks the kernel version, reads the running kernel configuration
when the distribution exposes it, verifies the required tools, and performs
the authoritative test: it creates and removes temporary netkit and VRF
devices. Run the kernel checks manually with:

```sh
uname -r
grep -E '^(CONFIG_NETKIT|CONFIG_NET_VRF|CONFIG_NET_L3_MASTER_DEV)=' \
  "/boot/config-$(uname -r)"
sudo modprobe netkit  # needed only when CONFIG_NETKIT=m
sudo modprobe vrf     # needed only when CONFIG_NET_VRF=m
sudo ip link add nk-probe type netkit
sudo ip link delete nk-probe
sudo ip link add vrf-probe type vrf table 4242
sudo ip link delete vrf-probe
```

Some distributions do not publish the kernel configuration file. Successful
device creation still proves that the running kernel supports the feature.

## Architecture

Each node has one VRF and one node-scoped transit netkit pair:

```text
                    default routing domain (table main)

       remote PodCIDR via remote Node InternalIP on the node underlay
                                  |
                             [ nk-main ]
                           169.254.100.1/30
                                  ||
                         L3 netkit transit pair
                                  ||
                            [ nk-vrf-x ]
                           169.254.100.2/30
                                  |
                         master: nk-vrf (table 100)

                       VRF routing domain (table 100)
                         /32 dev nkh<hash>
                                  |
                     primary L3 netkit (FORWARD)
                                  ||
                       peer L3 netkit (FORWARD)
                                  |
                       Pod eth0: 10.244.x.y/32
                         default dev eth0
```

The link-local transit addresses make the two sides explicit and inspectable.
Routes crossing the transit are **device routes**, not gateway routes. This is
intentional: an L3 netkit has no Ethernet neighbor resolution, so treating the
peer address as an ARP-resolved next hop would be the wrong model.

### CNI binary

For `ADD`, the `netkit-cni` binary:

1. delegates allocation to the configured `host-local` plugin;
2. requires exactly one IPv4 allocation and changes its prefix to `/32`;
3. derives a stable, collision-resistant host name from SHA-256 of
   `CNI_CONTAINERID` (`nkh` plus 12 hex characters, exactly 15 bytes);
4. creates the primary and peer as one L3 netkit pair, placing the peer directly
   in `CNI_NETNS` with `CNI_IFNAME`;
5. uses `FORWARD` policy on both ends and attaches no BPF program;
6. enslaves the host primary to `nk-vrf`, brings both links up, assigns the Pod
   `/32`, and installs `default dev <ifName>` in the Pod;
7. installs `<Pod IP>/32 dev <host netkit>` in VRF table 100 with route protocol
   99; and
8. returns a CNI 1.0.0 or 1.1.0 result.

Every failure after allocation rolls back the netkit pair and calls IPAM
`DEL`. `DEL` deletes by deterministic host name and always calls IPAM `DEL`,
even when the target namespace no longer exists. Repeated `DEL` calls tolerate
an already-missing netkit. `CHECK` asks IPAM to check its state and verifies the
netkit type, primary/peer role, L3 mode, policies, link state, VRF master, Pod
address, default device route, and host `/32` route against `prevResult`.

### Node agent

The privileged, host-networked `netkit-agent`:

- uses `client-go` in-cluster credentials;
- lists all Nodes and rejects absent or non-IPv4 `PodCIDR` and `InternalIP`
  values;
- enables IPv4 forwarding and disables reverse-path filtering on the global
  and transit paths;
- enables `tcp_l3mdev_accept` and `udp_l3mdev_accept` so default-domain host
  sockets such as kubelet probes can communicate through the Pod VRF;
- creates `nk-vrf` with table 100;
- creates the `nk-main` / `nk-vrf-x` L3 netkit transit and enslaves only
  `nk-vrf-x` to the VRF;
- writes protocol-98 routes:
  - main table: local Node PodCIDR `dev nk-main`;
  - main table: every remote Node PodCIDR via that Node's InternalIP;
  - VRF table: the smallest aggregate containing all current Node PodCIDRs
    `dev nk-vrf-x`;
  - VRF table: a default route `dev nk-vrf-x` so replies to node-underlay
    addresses and Pod connections to the Kubernetes API leak back to the main
    table;
- removes stale protocol-98 routes while leaving connected, Kubernetes, and
  per-Pod protocol-99 routes alone; and
- reconciles priority-500 destination policy for the local PodCIDR into table
  100, giving host-to-Pod traffic a direct route through the Pod's netkit;
- atomically replaces `/etc/cni/net.d/10-netkit-cni.conf` with a `host-local`
  range for that Node's PodCIDR.

Reconciliation repeats every five seconds in the example DaemonSet. Invalid
API data, conflicting links, partial transit pairs, and netlink/sysctl failures
are logged as explicit reconciliation errors; the agent does not silently
choose another datapath.

## Packet walks

### Pod to Pod on the same node

1. The source Pod sends to its gateway-free default route over the netkit peer.
2. The host primary receives in the VRF domain.
3. Table 100's cluster aggregate sends the packet over `nk-vrf-x`.
4. `nk-main` receives in the default routing domain.
5. The main table's local PodCIDR route sends it back over `nk-main`.
6. `nk-vrf-x` receives in the VRF, where the destination `/32` selects the
   destination Pod's host netkit.

The transit is crossed twice. This is deliberately simple route leaking, not a
performance claim.

Traffic outside the Pod aggregate uses table 100's default transit route. This
is required for Pod access to the API server and other node-underlay
destinations. It also means the VRF is an explicit routing domain, not a
security boundary: this educational CNI does not implement egress policy.

Host-to-Pod traffic follows the priority-500 local-PodCIDR rule directly into
table 100 instead of crossing the transit pair. Linux scopes sockets by VRF,
so the agent also enables `tcp_l3mdev_accept` and `udp_l3mdev_accept`. The
kernel documentation warns that, when identically bound VRF-aware and unbound
services coexist, selection is unspecified; this lab does not run duplicate
host services.

### Pod to Pod on different nodes

1. Steps 1-4 above leak the packet from the source VRF to the main table.
2. The remote PodCIDR route sends the unchanged packet to the remote Node
   InternalIP over the existing node underlay.
3. The remote node receives it in the main table and uses its local PodCIDR
   route to cross `nk-main` into table 100.
4. The remote VRF's Pod `/32` sends it through the destination netkit pair.

The return path is symmetric. There is no SNAT; the destination observes the
source Pod IP.

## Build and test

```sh
make test       # unit tests
make vet        # static checks
make build      # Linux netkit-cni, netkit-agent, and host-local binaries
make check      # test + vet
```

On a suitable privileged Linux host:

```sh
sudo make integration
```

The focused integration test creates a real network namespace and VRF, invokes
the CNI protocol directly, checks L3 netkit state, the Pod `/32`, its
gateway-free default route, the VRF `/32`, and then deletes the namespace
before `DEL` to prove IPAM cleanup still occurs.

### Three-node kind test

The kind configuration disables the default CNI and uses one control-plane and
two workers. The setup script builds `netkit-cni:dev`, loads it into every kind
node, and uses `imagePullPolicy: Never`; no published project image is needed.
The node image is pinned to the multi-architecture image published with kind
v0.33.0:

```text
kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
```

Run:

```sh
make kind-up
make e2e
make kind-down
```

`make e2e` places two Pods on one worker and a pair across both workers. It
tests both directions for both pairs. Each destination returns the TCP peer
address it observed, and the script requires that value to equal the source
Pod IP, proving that the tested flows were not SNATed. It also inspects netkit
details, the VRF, protocol-98 node routes, table 100, protocol-99 Pod `/32`
routes, and forwarding on all three nodes.

## Inspection

Inside a kind node:

```sh
docker exec netkit-cni-worker ip -details link show type netkit
docker exec netkit-cni-worker ip -details link show type vrf
docker exec netkit-cni-worker ip -4 route show table main proto 98
docker exec netkit-cni-worker ip -4 route show table 100
docker exec netkit-cni-worker cat /etc/cni/net.d/10-netkit-cni.conf
```

Useful host checks:

```sh
sysctl net.ipv4.ip_forward
sysctl net.ipv4.conf.all.rp_filter
ip rule show
ip -4 route get 10.244.2.2
```

## Troubleshooting

### `operation not supported` while creating netkit

The running kernel lacks netkit support. Verify `uname -r` and
`CONFIG_NETKIT`. Updating the kind node image does not update Docker Desktop's
VM kernel.

### Nodes stay `NotReady`

Inspect the agent before looking at application Pods:

```sh
kubectl -n kube-system get pods -l app.kubernetes.io/name=netkit-cni -o wide
kubectl -n kube-system logs daemonset/netkit-agent -c agent
kubectl -n kube-system logs daemonset/netkit-agent -c install-cni-binaries
```

Common causes are missing kernel features, an existing link named `nk-vrf`,
`nk-main`, or `nk-vrf-x` with incompatible type, and a Node without an assigned
IPv4 PodCIDR.

### One-way or cross-node connectivity failure

Confirm:

- every Node InternalIP is directly reachable from every other node;
- remote PodCIDRs exist as protocol-98 main-table routes;
- forwarding is 1;
- reverse-path filtering is 0 on `all`, `default`, and both transit links; and
- firewalls allow Pod CIDRs. This project does not manage nftables or iptables.

### CNI `CHECK` says `prevResult` is missing

CNI 1.x runtimes must supply the result being checked. The plugin intentionally
does not invent expected state because that could turn a broken check into a
success-shaped result.

## Limitations and security

This project is deliberately non-production:

- no NetworkPolicy or workload firewall;
- no encryption, overlay, MTU discovery, or underlay capability detection;
- no IPv6, dual stack, multiple IPs, bandwidth, port mappings, or DNS policy;
- no route ownership database beyond dedicated protocols 98 and 99;
- no cleanup of orphaned host-local allocations after a hard node failure;
- no collision recovery beyond a 48-bit truncated SHA-256 interface name;
- no leader election or event-driven informer;
- no graceful migration from another CNI;
- a privileged host-network DaemonSet can alter host networking and install
  host binaries; compromise of it is equivalent to node compromise; and
- unencapsulated Pod traffic requires the node underlay and its security rules
  to carry every PodCIDR.

Do not deploy this into a shared or sensitive cluster.

## Dependencies

The module pins released versions:

- `github.com/containernetworking/cni` v1.3.1;
- `github.com/containernetworking/plugins` v1.9.1;
- `github.com/vishvananda/netlink` v1.3.1, including netkit support; and
- Kubernetes `client-go`, API, and apimachinery v0.37.1.

## References

- [CNI specification 1.1.0](https://www.cni.dev/docs/spec/)
- [Linux netkit source](https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/tree/drivers/net/netkit.c)
- Linux commit
  [`35dfaad7188c`](https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/commit/?id=35dfaad7188cdc043fde31709c796f5a692ba2bd),
  “netkit, bpf: Add bpf programmable net device”
- [Linux VRF documentation](https://docs.kernel.org/networking/vrf.html)
- [Cilium netkit device mode and tuning](https://docs.cilium.io/en/stable/operations/performance/tuning/#netkit-device-mode)
- [Calico routed datapath](https://docs.tigera.io/calico/latest/reference/architecture/data-path)
- [kind node image guidance](https://kind.sigs.k8s.io/docs/user/quick-start/#creating-a-cluster)
