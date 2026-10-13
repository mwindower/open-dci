// Package metalstack derives open-dci networks from metal-apiserver's private
// networks (API v2), without metal-stack entities of its own: all networks of
// a project, or (exclusively) the networks of a project sharing a label
// value, form one stitched network (a group). Groups never span projects. The mapping is deterministic, so both gateways
// of a partition derive the same networks, SIDs and route targets.
package metalstack

import (
	"fmt"
	"hash/fnv"
	"maps"
	"net/netip"
	"slices"
	"sort"

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

// member is a private network that belongs to a group.
type member struct {
	id, partition string
	vni           uint32
	prefixes      []string
	defaultRoute  string
}

type group struct {
	name    string
	members []member
}

// Map turns metal-apiserver networks into the gateway's networks: one per
// group with a member in the own partition and at least one in another.
// Groups that can't be stitched are skipped with a reason; they never stop
// the others.
func Map(nets []*apiv2.Network, src *config.MetalStackSource) ([]config.Network, []config.Skipped) {
	var skipped []config.Skipped
	skip := func(name, format string, a ...any) {
		skipped = append(skipped, config.Skipped{Group: name, Reason: fmt.Sprintf(format, a...)})
	}
	allProjects := slices.Contains(src.Projects, config.AllProjects)
	groups := map[string]*group{}
	for _, n := range nets {
		if n.GetType() != apiv2.NetworkType_NETWORK_TYPE_CHILD || n.Vrf == nil || n.GetProject() == "" || n.GetPartition() == "" {
			continue
		}
		// a group never spans projects
		labels := n.GetMeta().GetLabels().GetLabels()
		var name string
		switch {
		case src.Label != "":
			v := labels[src.Label]
			if v == "" {
				continue
			}
			name = "label/" + n.GetProject() + "/" + v
		case allProjects || slices.Contains(src.Projects, n.GetProject()):
			name = "project/" + n.GetProject()
		default:
			continue
		}
		g := groups[name]
		if g == nil {
			g = &group{name: name}
			groups[name] = g
		}
		m := member{id: n.GetId(), partition: n.GetPartition(), vni: n.GetVrf(), prefixes: n.GetPrefixes()}
		if src.DefaultRouteLabel != "" {
			m.defaultRoute = labels[src.DefaultRouteLabel]
		}
		g.members = append(g.members, m)
	}

	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)

	// route targets must be unique across all groups, not only the own ones
	rtOwner := map[uint32][]string{}
	for _, name := range names {
		rt := RouteTargetLocal(name)
		rtOwner[rt] = append(rtOwner[rt], name)
	}

	var out []config.Network
	for _, name := range names {
		g := groups[name]
		sort.Slice(g.members, func(i, j int) bool { return g.members[i].id < g.members[j].id })
		var own *member
		partitions := map[string]int{}
		for i, m := range g.members {
			partitions[m.partition]++
			if m.partition == src.Partition {
				own = &g.members[i]
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
			skip(name, "%d networks in partition %s, at most one per partition (label them to stitch them separately)", partitions[crowded], crowded)
			continue
		case len(partitions) < 2:
			skip(name, "no network in another partition, nothing to stitch")
			continue
		}
		rt := RouteTargetLocal(name)
		if owners := rtOwner[rt]; len(owners) > 1 {
			skip(name, "route target %d:%d collides with %v", src.RouteTargetASN, rt, owners)
			continue
		}
		// IPv4 first, then by address: independent of the API's order
		var prefixes []netip.Prefix
		for _, m := range g.members {
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
				return map[bool]int{true: -1, false: 1}[a.Addr().Is4()]
			}
			return a.Compare(b)
		})
		var aggs []string
		for _, p := range prefixes {
			aggs = append(aggs, p.String())
		}
		out = append(out, config.Network{
			Group:        name,
			VRF:          fmt.Sprintf("vrf%d", own.vni),
			VNI:          own.vni,
			RouteTarget:  fmt.Sprintf("%d:%d", src.RouteTargetASN, rt),
			Aggregates:   aggs,
			DefaultRoute: own.defaultRoute,
		})
	}
	return out, skipped
}

// AssignSIDs pins the SID function of networks that have none: the VNI if it
// fits 16 bits and is free, otherwise a hash of the group, probing upwards.
// static networks keep theirs. Networks are handled in group order, so both
// gateways of a partition assign the same SIDs.
func AssignSIDs(nets []config.Network, static []config.Network) {
	used := map[uint32]bool{}
	for _, n := range static {
		used[staticSID(n)] = true
	}
	idx := make([]int, len(nets))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return nets[idx[a]].Group < nets[idx[b]].Group })
	var later []int
	for _, i := range idx {
		n := &nets[i]
		switch {
		case n.SID != 0:
			used[n.SID] = true
		case n.VNI <= config.MaxSIDFunction && !used[n.VNI]:
			n.SID = n.VNI
			used[n.SID] = true
		default:
			later = append(later, i)
		}
	}
	for _, i := range later {
		n := &nets[i]
		h := fnv.New32a()
		h.Write([]byte(n.Group))
		sid := h.Sum32()%config.MaxSIDFunction + 1
		for used[sid] {
			sid = sid%config.MaxSIDFunction + 1
		}
		n.SID = sid
		used[sid] = true
	}
}

// staticSID is a static network's SID after defaulting.
func staticSID(n config.Network) uint32 {
	if n.SID == 0 && n.VNI <= config.MaxSIDFunction {
		return n.VNI
	}
	return n.SID
}
