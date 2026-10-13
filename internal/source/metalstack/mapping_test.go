package metalstack

import (
	"fmt"
	"strings"
	"testing"

	apiv2 "github.com/metal-stack/api/go/metalstack/api/v2"

	"github.com/mwindower/open-dci/internal/config"
)

func network(id, project, partition string, vrf uint32, prefix string, labels map[string]string) *apiv2.Network {
	n := &apiv2.Network{
		Id: id, Project: &project, Partition: &partition, Vrf: &vrf,
		Type: apiv2.NetworkType_NETWORK_TYPE_CHILD, Prefixes: []string{prefix},
	}
	if labels != nil {
		n.Meta = &apiv2.Meta{Labels: &apiv2.Labels{Labels: labels}}
	}
	return n
}

func byProject(partition string, projects ...string) *config.MetalStackSource {
	return &config.MetalStackSource{Partition: partition, RouteTargetASN: 65535, Projects: projects, DefaultRouteLabel: "dci/default"}
}

func byLabel(partition string) *config.MetalStackSource {
	return &config.MetalStackSource{Partition: partition, RouteTargetASN: 65535, Label: "dci/stitch", DefaultRouteLabel: "dci/default"}
}

func TestRouteTargetLocal(t *testing.T) {
	seen := map[uint32]string{}
	for i := range 1000 {
		g := fmt.Sprintf("project/p%d", i)
		rt := RouteTargetLocal(g)
		if rt < 1<<24 {
			t.Fatalf("%s: %d is in the VNI range", g, rt)
		}
		if other, ok := seen[rt]; ok {
			t.Fatalf("%s and %s collide", g, other)
		}
		seen[rt] = g
	}
	// fixed: changing the hash renumbers every stitched network
	if got := RouteTargetLocal("project/p2"); got != RouteTargetLocal("project/p2") || got == RouteTargetLocal("project/p3") {
		t.Fatal("not deterministic")
	}
}

func TestMapProjects(t *testing.T) {
	nets := []*apiv2.Network{
		network("n-a", "p1", "a", 3982, "10.0.17.0/24", map[string]string{"dci/default": "import"}),
		network("n-b", "p1", "b", 4012, "10.0.33.0/24", nil),
		network("n-c", "p1", "c", 5012, "10.0.49.0/24", nil),
		network("other", "p9", "a", 4000, "10.9.0.0/24", nil),      // project not selected
		network("lonely", "p2", "a", 4100, "10.2.0.0/24", nil),     // only in partition a
		network("twice1", "p3", "a", 4201, "10.3.1.0/24", nil),     // two in partition a
		network("twice2", "p3", "a", 4202, "10.3.2.0/24", nil),     //
		network("twice3", "p3", "b", 4203, "10.3.3.0/24", nil),     //
		network("elsewhere", "p4", "b", 4300, "10.4.0.0/24", nil),  // not in partition a
		network("elsewhere2", "p4", "c", 4301, "10.4.1.0/24", nil), //
	}
	shared := network("shared", "p1", "b", 4500, "10.5.0.0/24", nil)
	shared.Type = apiv2.NetworkType_NETWORK_TYPE_CHILD_SHARED
	nets = append(nets, shared)

	got, skipped := Map(nets, byProject("a", "p1", "p2", "p3", "p4"))
	if len(got) != 1 {
		t.Fatalf("want one network, got %+v", got)
	}
	n := got[0]
	want := config.Network{
		Group: "project/p1", VRF: "vrf3982", VNI: 3982, DefaultRoute: "import",
		RouteTarget: fmt.Sprintf("65535:%d", RouteTargetLocal("project/p1")),
		Aggregates:  []string{"10.0.17.0/24", "10.0.33.0/24", "10.0.49.0/24"},
	}
	if fmt.Sprint(n) != fmt.Sprint(want) {
		t.Fatalf("got  %+v\nwant %+v", n, want)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Group] = s.Reason
	}
	if !strings.Contains(reasons["project/p2"], "nothing to stitch") || !strings.Contains(reasons["project/p3"], "2 networks in partition a") || len(reasons) != 2 {
		t.Fatalf("skipped: %+v", skipped)
	}

	// all projects
	got, _ = Map(nets, byProject("b", config.AllProjects))
	var groups []string
	for _, n := range got {
		groups = append(groups, n.Group+"="+n.VRF)
	}
	if fmt.Sprint(groups) != "[project/p1=vrf4012 project/p4=vrf4300]" {
		t.Fatalf("all projects in partition b: %v", groups)
	}
}

func TestMapLabels(t *testing.T) {
	nets := []*apiv2.Network{
		network("a1", "p1", "a", 3981, "10.0.16.0/24", map[string]string{"dci/stitch": "prod"}),
		network("b1", "p1", "b", 4011, "10.0.32.0/24", map[string]string{"dci/stitch": "prod"}),
		network("a2", "p1", "a", 3982, "10.0.17.0/24", map[string]string{"dci/stitch": "dev"}),
		network("c2", "p1", "c", 5012, "10.0.49.0/24", map[string]string{"dci/stitch": "dev"}),
		network("b3", "p1", "b", 4013, "10.0.34.0/24", nil), // unlabelled: project mode
		network("c3", "p1", "c", 5013, "10.0.50.0/24", nil),
		network("x", "p2", "c", 5099, "10.9.0.0/24", map[string]string{"dci/stitch": "prod"}), // other project
	}
	got, skipped := Map(nets, byLabel("a"))
	if len(got) != 2 || len(skipped) != 0 {
		t.Fatalf("partition a: %+v %+v", got, skipped)
	}
	if got[0].Group != "label/p1/dev" || got[0].VRF != "vrf3982" || fmt.Sprint(got[0].Aggregates) != "[10.0.17.0/24 10.0.49.0/24]" {
		t.Fatalf("dev: %+v", got[0])
	}
	// p2's "prod" network in partition C is not part of p1's "prod"
	if got[1].Group != "label/p1/prod" || got[1].VRF != "vrf3981" || fmt.Sprint(got[1].Aggregates) != "[10.0.16.0/24 10.0.32.0/24]" {
		t.Fatalf("prod: %+v", got[1])
	}
	// label mode: unlabelled networks (b3, c3) aren't stitched; p2's
	// "prod" has no partner in its own project
	got, skipped = Map(nets, byLabel("c"))
	if len(got) != 1 || got[0].Group != "label/p1/dev" || got[0].VRF != "vrf5012" {
		t.Fatalf("partition c: %+v", got)
	}
	if len(skipped) != 1 || skipped[0].Group != "label/p2/prod" {
		t.Fatalf("partition c skipped: %+v", skipped)
	}
	// project mode ignores the labels: p1's five networks, two of them in
	// partition c, are no valid project group
	got, skipped = Map(nets, byProject("c", "p1"))
	if len(got) != 0 || len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "networks in partition") {
		t.Fatalf("project mode: %+v %+v", got, skipped)
	}
}

// The same label value in two projects makes two stitched networks.
func TestMapNoSharingAcrossProjects(t *testing.T) {
	nets := []*apiv2.Network{
		network("p1-a", "p1", "a", 3981, "10.1.16.0/24", map[string]string{"dci/stitch": "prod"}),
		network("p1-b", "p1", "b", 4011, "10.1.32.0/24", map[string]string{"dci/stitch": "prod"}),
		network("p2-a", "p2", "a", 3982, "10.2.16.0/24", map[string]string{"dci/stitch": "prod"}),
		network("p2-b", "p2", "b", 4012, "10.2.32.0/24", map[string]string{"dci/stitch": "prod"}),
	}
	for _, src := range []*config.MetalStackSource{byLabel("a"), byProject("a", config.AllProjects)} {
		got, skipped := Map(nets, src)
		if len(got) != 2 || len(skipped) != 0 || got[0].RouteTarget == got[1].RouteTarget {
			t.Fatalf("%+v: %+v %+v", src, got, skipped)
		}
		for _, n := range got {
			project := strings.Split(n.Group, "/")[1]
			for _, a := range n.Aggregates {
				if !strings.HasPrefix(a, "10."+project[1:]+".") {
					t.Errorf("%s has %s of another project", n.Group, a)
				}
			}
		}
	}
}

func TestAssignSIDs(t *testing.T) {
	static := []config.Network{{VRF: "vrf3981", VNI: 3981}, {VRF: "big", VNI: 100000, SID: 7}}
	nets := []config.Network{
		{Group: "g/c", VNI: 3981},   // VNI taken by a static network: hashed
		{Group: "g/a", VNI: 4011},   // VNI
		{Group: "g/b", VNI: 200000}, // too large: hashed
	}
	AssignSIDs(nets, static)
	if nets[1].SID != 4011 {
		t.Fatalf("g/a: %d", nets[1].SID)
	}
	seen := map[uint32]bool{3981: true, 7: true, 4011: true}
	for _, n := range []config.Network{nets[0], nets[2]} {
		if n.SID == 0 || n.SID > config.MaxSIDFunction || seen[n.SID] {
			t.Fatalf("%s: SID %d", n.Group, n.SID)
		}
		seen[n.SID] = true
	}
	// deterministic, independent of the order
	again := []config.Network{nets[2], nets[0], nets[1]}
	for i := range again {
		again[i].SID = 0
	}
	AssignSIDs(again, static)
	if again[0].SID != nets[2].SID || again[1].SID != nets[0].SID {
		t.Fatalf("order-dependent: %+v vs %+v", again, nets)
	}
}
