// packetflow renders docs/packet-flow.svg, the README animation: a tenant
// packet through the redundant gateway pairs at the exits. It is plain SVG + SMIL,
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
	name  string
	x, y  int
	sub   string // label below the box
	gw    bool   // gateway style
	side  [2]string
	right bool // side label to the right of the box (false: left)
}

// layout: two exits per partition, a redundant gateway pair attached to both
var nodes = []node{
	{name: "m-a", x: 46, y: 170, sub: "tenant 1"},
	{name: "leaf-a", x: 150, y: 170},
	{name: "exit-a1", x: 270, y: 140},
	{name: "exit-a2", x: 370, y: 205},
	{name: "gw-a1", x: 250, y: 275, gw: true},
	{name: "gw-a2", x: 330, y: 275, gw: true},
	{name: "core", x: 480, y: 170, sub: "IPv6 only"},
	{name: "exit-b1", x: 690, y: 140},
	{name: "exit-b2", x: 590, y: 205},
	{name: "gw-b1", x: 630, y: 275, gw: true},
	{name: "gw-b2", x: 710, y: 275, gw: true},
	{name: "leaf-b", x: 810, y: 170},
	{name: "m-b", x: 914, y: 170, sub: "tenant 1"},
}

var links = [][2]string{
	{"m-a", "leaf-a"}, {"leaf-a", "exit-a1"}, {"leaf-a", "exit-a2"},
	{"exit-a1", "gw-a1"}, {"exit-a1", "gw-a2"}, {"exit-a2", "gw-a1"}, {"exit-a2", "gw-a2"},
	{"exit-a1", "core"}, {"exit-a2", "core"}, {"core", "exit-b1"}, {"core", "exit-b2"},
	{"exit-b1", "gw-b1"}, {"exit-b1", "gw-b2"}, {"exit-b2", "gw-b1"}, {"exit-b2", "gw-b2"},
	{"exit-b1", "leaf-b"}, {"exit-b2", "leaf-b"}, {"leaf-b", "m-b"},
}

// labels below each gateway pair
var pairLabels = []struct {
	x          int
	line1, two string
}{
	{290, "anycast locator fd00:dc1:a::/48", "transport: DCI network"},
	{670, "anycast locator fd00:dc1:b::/48", "transport: underlay"},
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
	nodes             []string
	hops              []hop
}

var animations = []animation{
	{
		file:  "docs/packet-flow.svg",
		title: "Tenant VRFs stitched by redundant gateway pairs, dual-attached to two exits",
		aria: "Animated packet flow: each partition has two exits and a redundant gateway pair attached to both. A tenant " +
			"packet from m-a travels over VXLAN with VNI 3981 through leaf-a and exit-a1 to gw-a1, which provisioned the " +
			"tenant VRF. gw-a1 encapsulates it in SRv6 to partition B's anycast SID and sends it in the DCI network (VNI " +
			"104100) via the other exit, exit-a2, into the core. exit-b1 delivers it to gw-b2, which decapsulates it " +
			"(End.DT46) and forwards it with partition B's VNI 4011 via exit-b2 and leaf-b to m-b.",
		nodes: []string{"m-a", "leaf-a", "exit-a1", "exit-a2", "gw-a1", "gw-a2", "core", "gw-b1", "gw-b2", "exit-b1", "exit-b2", "leaf-b", "m-b"},
		hops: []hop{
			{[]string{"m-a", "leaf-a"}, []header{ip}, "m-a → leaf-a:", "A tenant machine sends a plain packet to its leaf."},
			{[]string{"leaf-a", "exit-a1", "gw-a1"}, []header{{"vx", "VXLAN  VNI 3981 (tenant 1, A)"}, ip}, "leaf-a → gw-a1:", "EVPN type-5 via either exit to either gateway (ECMP): both are VTEPs of VNI 3981."},
			{[]string{"gw-a1", "exit-a2"}, []header{{"dci", "VXLAN  VNI 104100 (DCI network)"}, {"sr", "SRv6  → fd00:dc1:b:fab:: (End.DT46)"}, ip}, "gw-a1 → exit-a2:", "SRv6 to B's anycast SID, in the DCI network via the gateway's other exit."},
			{[]string{"exit-a2", "core", "exit-b1"}, []header{{"sr", "SRv6  → fd00:dc1:b:fab::"}, ip}, "exit-a2 → exit-b1:", "Between partitions only IPv6: locators, no tenants, no VNIs."},
			{[]string{"exit-b1", "gw-b2"}, []header{{"sr", "SRv6  → fd00:dc1:b:fab::"}, ip}, "exit-b1 → gw-b2:", "Both gateways own the SID; a gateway or an exit can fail without BGP changes."},
			{[]string{"gw-b2", "exit-b2", "leaf-b"}, []header{{"vx", "VXLAN  VNI 4011 (tenant 1, B)"}, ip}, "gw-b2 → leaf-b:", "End.DT46 into the tenant VRF, type-5 with B's own VNI 4011."},
			{[]string{"leaf-b", "m-b"}, []header{ip}, "leaf-b → m-b:", "Delivered. Tenants never see SIDs or the transport."},
		},
	},
}

// timing in seconds: every hop moves, then dwells; the animation ends with a pause
const (
	width, height          = 960, 356
	nodeW, machineW, nodeH = 70, 64, 30
	move, dwell, pause     = 1.3, 1.1, 1.2
	packetAbove            = 26 // the packet dot sits this far above a node's centre
)

const style = `<style>
  text { font-family: ui-sans-serif, -apple-system, "Segoe UI", Helvetica, Arial, sans-serif; fill: #1f2328; }
  .scene { font-size: 13px; font-weight: 600; }
  .part { fill: #f6f8fa; stroke: #d0d7de; }
  .plabel { font-size: 12px; font-weight: 600; fill: #59636e; }
  .link { stroke: #8c959f; stroke-width: 2; }
  .node { fill: #ffffff; stroke: #59636e; stroke-width: 1.5; }
  .gw { fill: #fff1e5; stroke: #bc4c00; stroke-width: 2; }
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
	shownNode := map[string]bool{}
	for _, n := range a.nodes {
		shownNode[n] = true
	}
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
	w(`<rect class="part" x="8" y="104" width="400" height="216" rx="10"/><text class="plabel" x="20" y="124">Partition A · EVPN domain A</text>`)
	w(`<rect class="part" x="552" y="104" width="400" height="216" rx="10"/><text class="plabel" x="940" y="124" text-anchor="end">Partition B · EVPN domain B</text>`)
	for _, l := range links {
		if !shownNode[l[0]] || !shownNode[l[1]] {
			continue
		}
		p, q := find(l[0]), find(l[1])
		w(`<line class="link" x1="%d" y1="%d" x2="%d" y2="%d"/>`, p.x, p.y, q.x, q.y)
	}
	for _, n := range nodes {
		if !shownNode[n.name] {
			continue
		}
		nw, class := nodeW, "node"
		if strings.HasPrefix(n.name, "m-") {
			nw = machineW
		}
		if n.gw {
			class = "node gw"
		}
		w(`<rect class="%s" x="%.1f" y="%.1f" width="%d" height="%d" rx="6"/><text class="nl" x="%d" y="%d" text-anchor="middle">%s</text>`,
			class, float64(n.x)-float64(nw)/2, float64(n.y)-nodeH/2.0, nw, nodeH, n.x, n.y+4, n.name)
		if n.sub != "" {
			w(`<text class="ns" x="%d" y="%d" text-anchor="middle">%s</text>`, n.x, n.y+27, n.sub)
		}
		if n.side[0] != "" {
			sx, anchor := n.x-42, "end"
			if n.right {
				sx, anchor = n.x+42, "start"
			}
			w(`<text class="ns" x="%d" y="%d" text-anchor="%s">%s</text><text class="ns" x="%d" y="%d" text-anchor="%s">%s</text>`,
				sx, n.y-2, anchor, n.side[0], sx, n.y+10, anchor, n.side[1])
		}
	}
	for _, l := range pairLabels {
		w(`<text class="ns" x="%d" y="303" text-anchor="middle">%s</text><text class="ns" x="%d" y="314" text-anchor="middle">%s</text>`, l.x, l.line1, l.x, l.two)
	}
	w(`<text class="ns" x="480" y="312" text-anchor="middle">spines omitted</text>`)

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
		w("%s", shown(e.t0, e.t2, fmt.Sprintf(`<text class="cap" x="480" y="340" text-anchor="middle"><tspan font-weight="600">%d/%d  %s</tspan> %s</text>`, i+1, len(events), e.hop.bold, e.hop.caption)))
	}
	w("</svg>")
	return b.String(), dur
}
