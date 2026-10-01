// packetflow renders the README drawings: docs/packet-flow.svg, the animation
// of a tenant packet through the redundant gateway pairs at the exits, and
// docs/logical-view.svg (logical.go). It is plain SVG + SMIL,
// no scripts, because GitHub renders it via <img>.
//
// Usage (from the repository root): make docs-svg
package main

import (
	"fmt"
	"math"
	"os"
	"strings"
)

type node struct {
	name  string // key for links and hops
	label string // shown in the box
	x, y  int
	w     int    // box width
	sub   string // label below the box
	class string // "" (plain), "gw" or "fab" (the summarised fabric, dashed)
}

// partition centres
const ca, cb, cc = 160, 480, 800

// layout: the DCI network (the core) above three partitions. Each partition's
// two exits are one box, so are its redundant gateway pair (attached to both
// exits) and its fabric (spines, leaves, machines).
var nodes = func() []node {
	n := []node{{name: "core", label: "DCI network · IPv6 only", x: cb, y: 125, w: 180}}
	for _, p := range []struct {
		id string
		x  int
	}{{"a", ca}, {"b", cb}, {"c", cc}} {
		n = append(n,
			node{name: "exit-" + p.id, label: "exit-" + p.id + "1 · exit-" + p.id + "2", x: p.x, y: 215, w: 140},
			node{name: "fab-" + p.id, label: "fabric", x: p.x - 75, y: 295, w: 120, sub: "spines · leaves · machines", class: "fab"},
			node{name: "gw-" + p.id, label: "gw-" + p.id + "1 · gw-" + p.id + "2", x: p.x + 75, y: 295, w: 120, sub: "anycast pair", class: "gw"},
		)
	}
	return n
}()

var links = func() [][2]string {
	var l [][2]string
	for _, p := range []string{"a", "b", "c"} {
		l = append(l, [2]string{"core", "exit-" + p}, [2]string{"exit-" + p, "fab-" + p}, [2]string{"exit-" + p, "gw-" + p})
	}
	return l
}()

// one box per partition
var partitions = []struct {
	x            int
	name, detail string
}{
	{ca, "Partition A", "locator fd00:dc1:a::/48 · transport: EVPN VRF"},
	{cb, "Partition B", "locator fd00:dc1:b::/48 · transport: underlay"},
	{cc, "Partition C", "locator fd00:dc1:c::/48 · transport: underlay"},
}

// header is one encapsulation layer; class selects its colour
type header struct{ class, text string }

var ip = header{"ip", "IPv4  10.0.16.10 → 10.0.32.10"}

type hop struct {
	path    []string // nodes the packet passes, first to last
	headers []header // outer to inner
	bold    string
	caption string
}

// animation is one README animation: the packet's way through the lab.
type animation struct {
	file, title, aria string
	hops              []hop
}

var animations = []animation{
	{
		file:  "docs/packet-flow.svg",
		title: "Tenant VRFs stitched across the DCI network by redundant gateway pairs",
		aria:  "Animated packet flow across three partitions joined by the DCI network, an IPv6-only core. Each partition shows its two exits, its redundant gateway pair (attached to both exits) and its fabric (spines, leaves, machines) as one box each. A tenant packet comes from partition A's fabric over VXLAN with VNI 3981 via either exit to either gateway of pair A, which provisioned the tenant VRF. The gateway encapsulates it in SRv6 to partition B's anycast SID and sends it in partition A's EVPN VRF for the transport (VNI 104100) to either exit, into the DCI network. Partition B's exits deliver it to either gateway of pair B, which decapsulates it (End.DT46) and forwards it with partition B's VNI 4011 via the exits into partition B's fabric. Partition C works alike.",
		hops: []hop{
			{[]string{"fab-a", "exit-a", "gw-a"}, []header{{"vx", "VXLAN  VNI 3981 (tenant 1, A)"}, ip}, "fabric A → gateways A:", "EVPN type-5 via either exit to either gateway (ECMP): both are VTEPs of VNI 3981."},
			{[]string{"gw-a", "exit-a"}, []header{{"dci", "VXLAN  VNI 104100 (DCI VRF)"}, {"sr", "SRv6  → fd00:dc1:b:fab:: (End.DT46)"}, ip}, "gateways A → exits A:", "SRv6 to B's anycast SID, in A's EVPN VRF for the transport, to either exit."},
			{[]string{"exit-a", "core", "exit-b"}, []header{{"sr", "SRv6  → fd00:dc1:b:fab::"}, ip}, "exits A → exits B:", "The DCI network is plain IPv6: locators, no tenants, no VNIs. C works alike."},
			{[]string{"exit-b", "gw-b"}, []header{{"sr", "SRv6  → fd00:dc1:b:fab::"}, ip}, "exits B → gateways B:", "Both gateways own the SID; either one, or either exit, may fail."},
			{[]string{"gw-b", "exit-b", "fab-b"}, []header{{"vx", "VXLAN  VNI 4011 (tenant 1, B)"}, ip}, "gateways B → fabric B:", "End.DT46 into the tenant VRF, type-5 with B's own VNI 4011. Tenants never see SIDs."},
		},
	},
}

// timing in seconds: every hop moves, then dwells; the animation ends with a pause
const (
	width, height      = 960, 410
	nodeH              = 30
	move, dwell, pause = 1.3, 1.1, 1.2
	packetAbove        = 26 // the packet dot sits this far above a node's centre
)

const style = `<style>
  text { font-family: ui-sans-serif, -apple-system, "Segoe UI", Helvetica, Arial, sans-serif; fill: #1f2328; }
  .scene { font-size: 13px; font-weight: 600; }
  .part { fill: #f6f8fa; stroke: #d0d7de; }
  .plabel { font-size: 12px; font-weight: 600; fill: #59636e; }
  .link { stroke: #8c959f; stroke-width: 2; }
  .node { fill: #ffffff; stroke: #59636e; stroke-width: 1.5; }
  .gw { fill: #fff1e5; stroke: #bc4c00; stroke-width: 2; }
  .fab { stroke-dasharray: 5 4; }
  .nl { font-size: 12px; font-weight: 600; }
  .ns { font-size: 10px; fill: #59636e; }
  .h { stroke: rgba(0,0,0,.25); }
  .ip { fill: #dafbe1; } .vx { fill: #ddf4ff; } .dci { fill: #eddeff; } .sr { fill: #ffe2cc; }
  .ht { font-size: 10.5px; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
  .cap { font-size: 13px; }
  .pkt { fill: #bc4c00; }
  @media (prefers-color-scheme: dark) {
    text { fill: #e6edf3; }
    .part { fill: #161b22; stroke: #3d444d; }
    .plabel, .ns { fill: #9198a1; }
    .link { stroke: #656c76; }
    .node { fill: #0d1117; stroke: #9198a1; }
    .gw { fill: #3d1d00; stroke: #f0883e; }
    .h { stroke: rgba(255,255,255,.2); }
    .ht { fill: #0d1117; }
    .pkt { fill: #f0883e; }
  }
</style>`

// event is a hop on the timeline: it starts at t0 and is shown until t2.
type event struct {
	t0, t2 float64
	hop    hop
}

func main() {
	for _, a := range animations {
		svg, dur := render(a)
		if err := os.WriteFile(a.file, []byte(svg), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("%s: %.1fs\n", a.file, dur)
	}
	if err := os.WriteFile(logicalFile, []byte(renderLogical()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(logicalFile)
}

func find(name string) node {
	for _, n := range nodes {
		if n.name == name {
			return n
		}
	}
	panic("unknown node " + name)
}

// render returns the SVG and the duration of one animation cycle.
func render(a animation) (string, float64) {
	var events []event
	t := 0.0
	for i, h := range a.hops {
		t2 := t + move + dwell
		if i+1 == len(a.hops) {
			t2 += pause
		}
		events = append(events, event{t, t2, h})
		t = t2
	}
	dur := t
	kt := func(x float64) string { return fmt.Sprintf("%.4f", x/dur) }
	durAttr := fmt.Sprintf(`dur="%.1fs" repeatCount="indefinite"`, dur)

	// shown wraps inner so that it is visible from t0 to t2 (discrete opacity)
	shown := func(t0, t2 float64, inner string) string {
		switch {
		case t0 == 0:
			return fmt.Sprintf(`<g opacity="1"><animate attributeName="opacity" %s calcMode="discrete" values="1;0" keyTimes="0;%s"/>%s</g>`, durAttr, kt(t2), inner)
		case math.Abs(t2-dur) < 1e-9:
			return fmt.Sprintf(`<g opacity="0"><animate attributeName="opacity" %s calcMode="discrete" values="0;1" keyTimes="0;%s"/>%s</g>`, durAttr, kt(t0), inner)
		}
		return fmt.Sprintf(`<g opacity="0"><animate attributeName="opacity" %s calcMode="discrete" values="0;1;0" keyTimes="0;%s;%s"/>%s</g>`, durAttr, kt(t0), kt(t2), inner)
	}

	// packet waypoints; a scene change is a jump (two points at the same time)
	var values, keyTimes []string
	point := func(t float64, n node) {
		values = append(values, fmt.Sprintf("%d %d", n.x, n.y-packetAbove))
		keyTimes = append(keyTimes, kt(t))
	}
	for _, e := range events {
		var segs []float64
		total := 0.0
		for i := 1; i < len(e.hop.path); i++ {
			a, b := find(e.hop.path[i-1]), find(e.hop.path[i])
			d := math.Hypot(float64(b.x-a.x), float64(b.y-a.y))
			segs = append(segs, d)
			total += d
		}
		point(e.t0, find(e.hop.path[0]))
		acc := 0.0
		for i, d := range segs {
			acc += d
			point(e.t0+move*acc/total, find(e.hop.path[i+1]))
		}
		point(e.t2, find(e.hop.path[len(e.hop.path)-1]))
	}

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="%s">`, width, height, width, height, a.aria)
	w("%s", style)
	for _, p := range partitions {
		w(`<rect class="part" x="%d" y="165" width="304" height="207" rx="10"/><text class="plabel" x="%d" y="345" text-anchor="middle">%s</text><text class="ns" x="%d" y="360" text-anchor="middle">%s</text>`,
			p.x-152, p.x, p.name, p.x, p.detail)
	}
	for _, l := range links {
		p, q := find(l[0]), find(l[1])
		w(`<line class="link" x1="%d" y1="%d" x2="%d" y2="%d"/>`, p.x, p.y, q.x, q.y)
	}
	for _, n := range nodes {
		class := "node"
		switch n.class {
		case "gw":
			class = "node gw"
		case "fab":
			class = "node fab"
		}
		w(`<rect class="%s" x="%.1f" y="%.1f" width="%d" height="%d" rx="6"/><text class="nl" x="%d" y="%d" text-anchor="middle">%s</text>`,
			class, float64(n.x)-float64(n.w)/2, float64(n.y)-nodeH/2.0, n.w, nodeH, n.x, n.y+4, n.label)
		if n.sub != "" {
			w(`<text class="ns" x="%d" y="%d" text-anchor="middle">%s</text>`, n.x, n.y+27, n.sub)
		}
	}

	w(`<text class="scene" x="480" y="26" text-anchor="middle">%s</text>`, a.title)
	// header stack: a fixed panel, the innermost header at the bottom
	for _, e := range events {
		var inner strings.Builder
		for i := range e.hop.headers {
			h := e.hop.headers[len(e.hop.headers)-1-i]
			y := 80 - 18*i
			fmt.Fprintf(&inner, `<rect class="h %s" x="345" y="%d" width="270" height="16" rx="3"/><text class="ht" x="480" y="%d" text-anchor="middle">%s</text>`, h.class, y, y+12, h.text)
		}
		w("%s", shown(e.t0, e.t2, inner.String()))
	}
	w(`<g><animateTransform attributeName="transform" type="translate" %s values="%s" keyTimes="%s"/><circle class="pkt" cx="0" cy="0" r="6"/></g>`,
		durAttr, strings.Join(values, ";"), strings.Join(keyTimes, ";"))
	for i, e := range events {
		w("%s", shown(e.t0, e.t2, fmt.Sprintf(`<text class="cap" x="480" y="398" text-anchor="middle"><tspan font-weight="600">%d/%d  %s</tspan> %s</text>`, i+1, len(events), e.hop.bold, e.hop.caption)))
	}
	w("</svg>")
	return b.String(), dur
}
