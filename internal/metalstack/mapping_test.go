package metalstack

import (
	"fmt"
	"strings"
	"testing"

	apiv2 "github.com/metal-stack/api/go/metalstack/api/v2"

	"github.com/mwindower/open-dci/internal/config"
)

const stitch = "dci/stitch"

func network(id, project, partition string, vrf uint32, prefix string, labels map[string]string) *apiv2.Network {
	return &apiv2.Network{
		Id: id, Project: &project, Partition: &partition, Vrf: &vrf,
		Type: apiv2.NetworkType_NETWORK_TYPE_CHILD, Prefixes: []string{prefix},
		Meta: &apiv2.Meta{Labels: &apiv2.Labels{Labels: labels}},
	}
}

func labelled(v string) map[string]string { return map[string]string{stitch: v} }

var opts = Options{RouteTargetASN: 65535, Label: stitch, DefaultRouteLabel: "dci/default"}

func groupsOf(nets []Network) string {
	var g []string
	for _, n := range nets {
		g = append(g, n.Group+"="+n.VRF)
	}
	return strings.Join(g, " ")
}

func TestRouteTargetLocal(t *testing.T) {
	seen := map[uint32]string{}
	for i := range 1000 {
		g := fmt.Sprintf("p%d/main", i)
		rt := RouteTargetLocal(g)
		if rt < 1<<24 {
			t.Fatalf("%s: %d is in the VNI range", g, rt)
		}
		if other, ok := seen[rt]; ok {
			t.Fatalf("%s and %s collide", g, other)
		}
		seen[rt] = g
	}
}

func TestMap(t *testing.T) {
	nets := []*apiv2.Network{
		network("a1", "p1", "a", 3981, "10.0.16.0/24", map[string]string{stitch: "prod", "dci/default": "import"}),
		network("b1", "p1", "b", 4011, "10.0.32.0/24", labelled("prod")),
		network("a2", "p1", "a", 3982, "10.0.17.0/24", labelled("dev")),
		network("c2", "p1", "c", 5012, "10.0.49.0/24", labelled("dev")),
		network("b3", "p1", "b", 4013, "10.0.34.0/24", nil),              // unlabelled: not stitched
		network("a4", "p1", "a", 3984, "10.0.19.0/24", labelled("solo")), // only in partition a: stitched anyway
		network("x", "p2", "c", 5099, "10.9.0.0/24", labelled("prod")),   // same value, other project
		network("d1", "p3", "a", 3990, "10.3.16.0/24", labelled("dup")),  // two in partition c
		network("d2", "p3", "c", 5091, "10.3.48.0/24", labelled("dup")),  //
		network("d3", "p3", "c", 5092, "10.3.49.0/24", labelled("dup")),  //
	}
	shared := network("shared", "p1", "b", 4500, "10.5.0.0/24", labelled("prod"))
	shared.Type = apiv2.NetworkType_NETWORK_TYPE_CHILD_SHARED
	nets = append(nets, shared)

	got, skipped := Map(nets, opts, "a")
	if g := groupsOf(got); g != "p1/dev=vrf3982 p1/prod=vrf3981 p1/solo=vrf3984" {
		t.Fatalf("partition a: %s", g)
	}
	prod := got[1]
	want := config.Network{
		Group: "p1/prod", VRF: "vrf3981", VNI: 3981, DefaultRoute: "import",
		RouteTarget: fmt.Sprintf("65535:%d", RouteTargetLocal("p1/prod")),
		Aggregates:  []string{"10.0.16.0/24", "10.0.32.0/24"}, // not p2's 10.9.0.0/24
	}
	if fmt.Sprint(prod.Network) != fmt.Sprint(want) {
		t.Fatalf("got  %+v\nwant %+v", prod, want)
	}
	if len(skipped) != 1 || skipped[0].Group != "p3/dup" || !strings.Contains(skipped[0].Reason, "2 networks in partition c") {
		t.Fatalf("skipped: %+v", skipped)
	}

	// p2's "prod" is its own group, alone in partition c
	got, _ = Map(nets, opts, "c")
	if g := groupsOf(got); g != "p1/dev=vrf5012 p2/prod=vrf5099" {
		t.Fatalf("partition c: %s", g)
	}
	if got[1].RouteTarget == fmt.Sprintf("65535:%d", RouteTargetLocal("p1/prod")) {
		t.Fatal("p2/prod shares p1/prod's route target")
	}
}

// A network joining or leaving a group changes only that group's
// aggregates, nothing of the others, and never the remaining members' VRFs.
func TestMapMembershipChanges(t *testing.T) {
	base := []*apiv2.Network{
		network("a", "p1", "a", 3981, "10.0.16.0/24", labelled("prod")),
		network("b", "p1", "b", 4011, "10.0.32.0/24", labelled("prod")),
		network("o", "p2", "a", 3982, "10.0.17.0/24", labelled("other")),
	}
	before, _ := Map(base, opts, "a")
	joined, _ := Map(append(base, network("c", "p1", "c", 5011, "10.0.48.0/24", labelled("prod"))), opts, "a")
	left, _ := Map([]*apiv2.Network{base[0], base[2]}, opts, "a") // without b: a is alone
	for _, after := range [][]Network{joined, left} {
		// sorted by group: p1/prod (the changing one), p2/other
		if len(after) != 2 || fmt.Sprint(after[1]) != fmt.Sprint(before[1]) {
			t.Fatalf("other group changed: %+v -> %+v", before[1], after[1])
		}
		if a, b := after[0], before[0]; a.VRF != b.VRF || a.VNI != b.VNI || a.RouteTarget != b.RouteTarget {
			t.Fatalf("member changed: %+v -> %+v", b, a)
		}
	}
	if fmt.Sprint(joined[0].Aggregates) != "[10.0.16.0/24 10.0.32.0/24 10.0.48.0/24]" || fmt.Sprint(left[0].Aggregates) != "[10.0.16.0/24]" {
		t.Fatalf("aggregates: %v %v", joined[0].Aggregates, left[0].Aggregates)
	}
}
