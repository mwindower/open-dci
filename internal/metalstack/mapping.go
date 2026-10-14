// Package metalstack derives open-dci networks from metal-apiserver's private
// networks (API v2), without metal-stack entities of its own: a project's
// networks that carry the stitch label with the same value form one stitched
// network (a group, "<project>/<value>"). Groups never span projects. The
// mapping is deterministic. dci-controller uses it to compute each
// partition's networks; the gateways assign the SIDs themselves.
//
// Label changes interrupt nothing else: a network joining or leaving a group
// only adds or removes its own prefixes elsewhere, and a group keeps its
// members even when only one partition is left.
package metalstack

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"maps"
	"net/netip"
	"slices"
	"time"

	apiv2 "github.com/metal-stack/api/go/metalstack/api/v2"

	"github.com/mwindower/open-dci/internal/config"
)

// rtBase keeps derived route targets clear of the VNI range (24 bits), which
// EVPN's auto route targets use as their local part.
const rtBase = 1 << 24

// RouteTargetLocal returns the derived local part of a group's route target:
// 2^24 + fnv32a(group) mod (2^32 - 2^24).
func RouteTargetLocal(group string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(group))
	return rtBase + h.Sum32()%(1<<32-rtBase)
}

// member is a labelled private network.
type member struct {
	id, partition string
	created       time.Time
	vni           uint32
	prefixes      []string
	defaultRoute  string
}

// Options say which networks are stitched and how route targets are formed.
type Options struct {
	// Label is the network label key: a project's networks with the same
	// value form one stitched network.
	Label string
	// DefaultRouteLabel is a label key whose value (export or import) sets
	// the network's defaultRoute.
	DefaultRouteLabel string
	// RouteTargetASN is the 2-byte admin part of the derived route targets.
	RouteTargetASN uint32
}

// Network is a network for one gateway, with the age of its metal-stack
// network (for the gateway's SID assignment: oldest first).
type Network struct {
	config.Network
	Created time.Time
}

// Map turns metal-apiserver networks into the networks of a partition's
// gateways: one per group with a member in that partition. Groups that
// can't be stitched are skipped with a reason (the same for every
// partition); they never stop the others. SIDs are left to the gateways.
func Map(nets []*apiv2.Network, opts Options, partition string) ([]Network, []config.Skipped) {
	var skipped []config.Skipped
	skip := func(name, format string, a ...any) {
		skipped = append(skipped, config.Skipped{Group: name, Reason: fmt.Sprintf(format, a...)})
	}
	groups := map[string][]member{}
	for _, n := range nets {
		if n.GetType() != apiv2.NetworkType_NETWORK_TYPE_CHILD || n.Vrf == nil || n.GetProject() == "" || n.GetPartition() == "" {
			continue
		}
		labels := n.GetMeta().GetLabels().GetLabels()
		v := labels[opts.Label]
		if v == "" {
			continue
		}
		// the project is part of the key: a group never spans projects
		name := n.GetProject() + "/" + v
		m := member{id: n.GetId(), partition: n.GetPartition(), vni: n.GetVrf(), prefixes: n.GetPrefixes()}
		if t := n.GetMeta().GetCreatedAt(); t != nil {
			m.created = t.AsTime()
		}
		if opts.DefaultRouteLabel != "" {
			m.defaultRoute = labels[opts.DefaultRouteLabel]
		}
		groups[name] = append(groups[name], m)
	}

	// route targets must be unique across all groups, not only the own ones
	rtOwner := map[uint32][]string{}
	for name := range groups {
		rt := RouteTargetLocal(name)
		rtOwner[rt] = append(rtOwner[rt], name)
	}

	var out []Network
	for _, name := range slices.Sorted(maps.Keys(groups)) {
		members := groups[name]
		slices.SortFunc(members, func(a, b member) int { return cmp.Compare(a.id, b.id) })
		var own *member
		partitions := map[string]int{}
		for i, m := range members {
			partitions[m.partition]++
			if m.partition == partition {
				own = &members[i]
			}
		}
		// the same decision on every gateway: a group with two networks in
		// one partition is stitched nowhere
		crowded := ""
		for _, p := range slices.Sorted(maps.Keys(partitions)) {
			if partitions[p] > 1 && crowded == "" {
				crowded = p
			}
		}
		switch {
		case own == nil:
			continue // not stitched here
		case crowded != "":
			skip(name, "%d networks in partition %s carry this label value, at most one per partition", partitions[crowded], crowded)
			continue
		}
		rt := RouteTargetLocal(name)
		if owners := rtOwner[rt]; len(owners) > 1 {
			slices.Sort(owners)
			skip(name, "route target %d:%d collides with %v", opts.RouteTargetASN, rt, owners)
			continue
		}
		out = append(out, Network{Created: own.created, Network: config.Network{
			Group:        name,
			VRF:          fmt.Sprintf("vrf%d", own.vni),
			VNI:          own.vni,
			RouteTarget:  fmt.Sprintf("%d:%d", opts.RouteTargetASN, rt),
			Aggregates:   aggregates(members),
			DefaultRoute: own.defaultRoute,
		}})
	}
	return out, skipped
}

// aggregates returns the members' prefixes, IPv4 first, then by address:
// independent of the API's order.
func aggregates(members []member) []string {
	var prefixes []netip.Prefix
	for _, m := range members {
		for _, p := range m.prefixes {
			pp, err := netip.ParsePrefix(p)
			if err != nil {
				continue // Validate can't see it, so check here
			}
			if pp = pp.Masked(); !slices.Contains(prefixes, pp) {
				prefixes = append(prefixes, pp)
			}
		}
	}
	slices.SortFunc(prefixes, func(a, b netip.Prefix) int {
		if a.Addr().Is4() != b.Addr().Is4() {
			if a.Addr().Is4() {
				return -1
			}
			return 1
		}
		return a.Compare(b)
	})
	var out []string
	for _, p := range prefixes {
		out = append(out, p.String())
	}
	return out
}
