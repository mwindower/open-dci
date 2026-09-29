// Package kernel holds the netlink primitives used by srv6-dci (and by the lab
// tooling). All functions are idempotent: they create what is missing and leave
// what already matches alone.
package kernel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// EnsureLink creates l if no link with its name exists, sets the MTU (if > 0)
// and brings it up. On return l.Attrs() reflects the kernel's view (index etc).
func EnsureLink(l netlink.Link, mtu int) error {
	if err := netlink.LinkAdd(l); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	got, err := netlink.LinkByName(l.Attrs().Name)
	if err != nil {
		return err
	}
	*l.Attrs() = *got.Attrs()
	if err := SetMTU(got, mtu); err != nil {
		return err
	}
	return netlink.LinkSetUp(got)
}

// SetMTU sets the MTU of l unless it already matches (or mtu is 0).
func SetMTU(l netlink.Link, mtu int) error {
	if mtu <= 0 || l.Attrs().MTU == mtu {
		return nil
	}
	if err := netlink.LinkSetMTU(l, mtu); err != nil {
		return fmt.Errorf("%s: mtu %d: %w", l.Attrs().Name, mtu, err)
	}
	l.Attrs().MTU = mtu
	return nil
}

// EnsureMaster enslaves l to master (e.g. a VRF) unless it already is.
func EnsureMaster(l netlink.Link, master string) error {
	m, err := netlink.LinkByName(master)
	if err != nil {
		return fmt.Errorf("%s: %w", master, err)
	}
	if l.Attrs().MasterIndex == m.Attrs().Index {
		return nil
	}
	return netlink.LinkSetMaster(l, m)
}

// EnsureAddrs adds the given CIDRs to l. IPv6 addresses are added without DAD.
func EnsureAddrs(l netlink.Link, cidrs ...string) error {
	for _, c := range cidrs {
		a, err := netlink.ParseAddr(c)
		if err != nil {
			return err
		}
		if a.IP.To4() == nil {
			a.Flags |= unix.IFA_F_NODAD
		}
		if err := netlink.AddrReplace(l, a); err != nil {
			return fmt.Errorf("%s: addr %s: %w", l.Attrs().Name, c, err)
		}
	}
	return nil
}

// Veth describes a veth pair; each end may live in a VRF ("" = default VRF).
type Veth struct {
	Name, VRF          string
	Addresses          []string
	Peer, PeerVRF      string
	PeerAddresses      []string
	MTU                int
	NoLinkLocalAutoGen bool // only use the configured (e.g. fixed fe80::) addresses
}

// EnsureVeth creates and configures a veth pair.
func EnsureVeth(v Veth) error {
	veth := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: v.Name}, PeerName: v.Peer}
	err := netlink.LinkAdd(veth)
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("veth %s: %w", v.Name, err)
	}
	created := err == nil
	ends := []struct {
		name, vrf string
		addrs     []string
	}{{v.Name, v.VRF, v.Addresses}, {v.Peer, v.PeerVRF, v.PeerAddresses}}
	for _, e := range ends {
		l, err := netlink.LinkByName(e.name)
		if err != nil {
			return err
		}
		// a fresh veth is still down: switch off the automatic fe80:: before first up
		if created && v.NoLinkLocalAutoGen {
			if err := sysctl(fmt.Sprintf("net.ipv6.conf.%s.addr_gen_mode", e.name), "1"); err != nil {
				return err
			}
		}
		if err := SetMTU(l, v.MTU); err != nil {
			return err
		}
		if e.vrf != "" {
			if err := EnsureMaster(l, e.vrf); err != nil {
				return err
			}
		}
		if err := EnsureAddrs(l, e.addrs...); err != nil {
			return err
		}
		if err := netlink.LinkSetUp(l); err != nil {
			return err
		}
	}
	return nil
}

// MoveLocalRule moves the "lookup local" policy rule from priority 0 to 32765,
// behind the l3mdev (VRF) rule, for IPv4 and IPv6. Without it, an address of
// the default VRF counts as local even for packets received inside a VRF.
func MoveLocalRule() error {
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		add := netlink.NewRule()
		add.Family, add.Table, add.Priority = family, unix.RT_TABLE_LOCAL, 32765
		if err := netlink.RuleAdd(add); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		del := netlink.NewRule()
		del.Family, del.Table, del.Priority = family, unix.RT_TABLE_LOCAL, 0
		if err := netlink.RuleDel(del); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	return nil
}

// LocalRuleLast reports whether no "lookup local" rule remains at priority 0.
func LocalRuleLast() (bool, error) {
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		rules, err := netlink.RuleList(family)
		if err != nil {
			return false, err
		}
		for _, r := range rules {
			if r.Table == unix.RT_TABLE_LOCAL && r.Priority == 0 {
				return false, nil
			}
		}
	}
	return true, nil
}

// Sysctl writes a sysctl (dotted key) unless it already has the value.
func Sysctl(key, value string) error { return sysctl(key, value) }

// GetSysctl reads a sysctl (dotted key).
func GetSysctl(key string) (string, error) {
	b, err := os.ReadFile(sysctlPath(key))
	return strings.TrimSpace(string(b)), err
}

func sysctl(key, value string) error {
	if cur, err := GetSysctl(key); err == nil && cur == value {
		return nil
	}
	if err := os.WriteFile(sysctlPath(key), []byte(value), 0o644); err != nil {
		return fmt.Errorf("sysctl %s=%s: %w", key, value, err)
	}
	return nil
}

// sysctlPath maps "net.ipv6.conf.dci0.x" to /proc/sys/net/ipv6/conf/dci0/x.
// Interface names containing dots are not supported.
func sysctlPath(key string) string {
	return filepath.Join("/proc/sys", strings.ReplaceAll(key, ".", "/"))
}

// IsVRF reports whether a link with the given name exists and is a VRF.
func IsVRF(name string) (bool, error) {
	l, err := netlink.LinkByName(name)
	if err != nil {
		var nf netlink.LinkNotFoundError
		if errors.As(err, &nf) {
			return false, nil
		}
		return false, err
	}
	return l.Type() == "vrf", nil
}
