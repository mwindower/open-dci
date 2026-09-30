// packetflow renders the README animations, one per gateway placement:
// docs/packet-flow-firewall.svg (tenant 1 through its metal-stack firewalls)
// and docs/packet-flow-gateway.svg (tenant 2 through dedicated gateways at the
// exits). They are plain SVG + SMIL, no scripts, because GitHub renders them
// via <img>.
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

// layout: firewalls hang off the leaves, dedicated gateways off the exits.
// Each animation shows a subset (and one machine per partition).
var nodes = []node{
	{name: "m-a", x: 46, y: 186, sub: "tenant 1"},
	{name: "m-a2", x: 46, y: 186, sub: "tenant 2"},
	{name: "leaf-a", x: 160, y: 186},
	{name: "fw-a", x: 160, y: 252, gw: true, side: [2]string{"firewall", "open-dci augments"}, right: true},
	{name: "exit-a", x: 330, y: 186},
	{name: "gw-a", x: 330, y: 252, gw: true, side: [2]string{"gateway", "open-dci provisions"}, right: true},
	{name: "core", x: 480, y: 186, sub: "IPv6 only"},
	{name: "exit-b", x: 630, y: 186},
	{name: "gw-b", x: 630, y: 252, gw: true, side: [2]string{"gateway", "open-dci provisions"}},
	{name: "leaf-b", x: 800, y: 186},
	{name: "fw-b", x: 800, y: 252, gw: true, side: [2]string{"firewall", "open-dci augments"}},
	{name: "m-b", x: 914, y: 186, sub: "tenant 1"},
	{name: "m-b2", x: 914, y: 186, sub: "tenant 2"},
}

var links = [][2]string{
	{"m-a", "leaf-a"}, {"m-a2", "leaf-a"}, {"leaf-a", "fw-a"}, {"leaf-a", "exit-a"},
	{"exit-a", "gw-a"}, {"exit-a", "core"}, {"core", "exit-b"}, {"exit-b", "gw-b"},
	{"exit-b", "leaf-b"}, {"leaf-b", "fw-b"}, {"leaf-b", "m-b"}, {"leaf-b", "m-b2"},
}

// header is one encapsulation layer; class selects its colour
type header struct{ class, text string }

var (
	ip1 = header{"ip", "IPv4  10.0.16.10 → 10.0.32.10"}
	ip2 = header{"ip", "IPv4  10.0.17.10 → 10.0.33.10"}
)

type hop struct {
	path    []string // nodes the packet passes, first to last
	headers []header // outer to inner
	bold    string
	caption string
}

// animation is one README animation: a placement and the packet's way through it.
type animation struct {
	file, title, aria string
	nodes             []string
	hops              []hop
}

var animations = []animation{
	{
		file:  "docs/packet-flow-firewall.svg",
		title: "On the tenant's firewall: open-dci augments its VRFs",
		aria: "Animated packet flow with open-dci on the tenant's metal-stack firewalls: a tenant packet from m-a travels " +
			"over VXLAN with VNI 3981 to fw-a, SRv6-encapsulated in the DCI network (VNI 104100) to the exit, as plain IPv6 " +
			"through the core and partition B's underlay to fw-b, which decapsulates it (End.DT46) and forwards it with " +
			"partition B's VNI 4011 to m-b.",
		nodes: []string{"m-a", "leaf-a", "fw-a", "exit-a", "core", "exit-b", "leaf-b", "fw-b", "m-b"},
		hops: []hop{
			{[]string{"m-a", "leaf-a"}, []header{ip1}, "m-a → leaf-a:", "A tenant-1 machine sends a plain packet to its leaf."},
			{[]string{"leaf-a", "fw-a"}, []header{{"vx", "VXLAN  VNI 3981 (tenant 1, A)"}, ip1}, "leaf-a → fw-a:", "EVPN type-5 to the tenant's metal-stack firewall."},
			{[]string{"fw-a", "leaf-a", "exit-a"}, []header{{"dci", "VXLAN  VNI 104100 (DCI network)"}, {"sr", "SRv6  → fd00:dc1:b:1:: (End.DT46)"}, ip1}, "fw-a → exit-a:", "SRv6 to fw-b's SID, carried to the exit in the DCI network."},
			{[]string{"exit-a", "core", "exit-b"}, []header{{"sr", "SRv6  → fd00:dc1:b:1::"}, ip1}, "exit-a → exit-b:", "Between partitions only IPv6: locators, no tenants, no VNIs."},
			{[]string{"exit-b", "leaf-b", "fw-b"}, []header{{"sr", "SRv6  → fd00:dc1:b:1::"}, ip1}, "exit-b → fw-b:", "Partition B's IPv6 underlay delivers it to fw-b, the SID's owner."},
			{[]string{"fw-b", "leaf-b"}, []header{{"vx", "VXLAN  VNI 4011 (tenant 1, B)"}, ip1}, "fw-b → leaf-b:", "End.DT46 into the tenant VRF, type-5 with B's own VNI 4011."},
			{[]string{"leaf-b", "m-b"}, []header{ip1}, "leaf-b → m-b:", "Delivered. No extra hardware, but tenant firewalls join the VPN."},
		},
	},
	{
		file:  "docs/packet-flow-gateway.svg",
		title: "Dedicated gateway at the exit: open-dci provisions the VRFs",
		aria: "Animated packet flow with dedicated gateways at the exits: a tenant packet from m-a2 travels over VXLAN with " +
			"VNI 3982 through the fabric to gw-a, whose tenant VRF open-dci provisioned, SRv6-encapsulated through exits and " +
			"core to gw-b, which decapsulates it (End.DT46) and forwards it with partition B's VNI 4012 via leaf-b to m-b2.",
		nodes: []string{"m-a2", "leaf-a", "exit-a", "gw-a", "core", "gw-b", "exit-b", "leaf-b", "m-b2"},
		hops: []hop{
			{[]string{"m-a2", "leaf-a"}, []header{ip2}, "m-a2 → leaf-a:", "A tenant-2 machine: its firewall runs no open-dci."},
			{[]string{"leaf-a", "exit-a", "gw-a"}, []header{{"vx", "VXLAN  VNI 3982 (tenant 2, A)"}, ip2}, "leaf-a → gw-a:", "Type-5 through the fabric to the gateway, the VTEP of VNI 3982."},
			{[]string{"gw-a", "exit-a", "core", "exit-b", "gw-b"}, []header{{"sr", "SRv6  → fd00:dc1:b2:1:: (End.DT46)"}, ip2}, "gw-a → gw-b:", "SRv6 in the default VRF, via the exits and the core."},
			{[]string{"gw-b", "exit-b", "leaf-b"}, []header{{"vx", "VXLAN  VNI 4012 (tenant 2, B)"}, ip2}, "gw-b → leaf-b:", "End.DT46 on gw-b, type-5 into the fabric with VNI 4012."},
			{[]string{"leaf-b", "m-b2"}, []header{ip2}, "leaf-b → m-b2:", "Delivered. Only provider gateways speak VPN and SRv6."},
		},
	},
}

// timing in seconds: every hop moves, then dwells; the animation ends with a pause
const (
	width, height          = 960, 330
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
	w(`<rect class="part" x="8" y="104" width="400" height="190" rx="10"/><text class="plabel" x="20" y="124">Partition A · EVPN domain A</text>`)
	w(`<rect class="part" x="552" y="104" width="400" height="190" rx="10"/><text class="plabel" x="940" y="124" text-anchor="end">Partition B · EVPN domain B</text>`)
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
	w(`<text class="ns" x="480" y="286" text-anchor="middle">spines omitted</text>`)

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
		w("%s", shown(e.t0, e.t2, fmt.Sprintf(`<text class="cap" x="480" y="314" text-anchor="middle"><tspan font-weight="600">%d/%d  %s</tspan> %s</text>`, i+1, len(events), e.hop.bold, e.hop.caption)))
	}
	w("</svg>")
	return b.String(), dur
}
