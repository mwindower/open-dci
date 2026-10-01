package main

import (
	"fmt"
	"strings"
)

// The logical view, docs/logical-view.svg: what a tenant gets. Each partition
// has its own private network and VNI per tenant; open-dci joins them into one
// routed network per tenant via SRv6 L3VPN. Static, no animation.

const logicalFile = "docs/logical-view.svg"

type network struct{ v4, v6, vni, sid string }

var logicalColumns = []struct {
	x            int
	name, detail string
	nets         [2]network // tenant 1, tenant 2
}{
	{ca, "Partition A", "gw-a1 + gw-a2 · locator fd00:dc1:a::/48", [2]network{
		{"10.0.16.0/24", "2001:db8:16::/48", "3981", "fd00:dc1:a:f8d::"},
		{"10.0.17.0/24", "2001:db8:17::/48", "3982", "fd00:dc1:a:f8e::"}}},
	{cb, "Partition B", "gw-b1 + gw-b2 · locator fd00:dc1:b::/48", [2]network{
		{"10.0.32.0/24", "2001:db8:32::/48", "4011", "fd00:dc1:b:fab::"},
		{"10.0.33.0/24", "2001:db8:33::/48", "4012", "fd00:dc1:b:fac::"}}},
	{cc, "Partition C", "gw-c1 + gw-c2 · locator fd00:dc1:c::/48", [2]network{
		{"10.0.48.0/24", "2001:db8:48::/48", "5011", "fd00:dc1:c:1393::"},
		{"10.0.49.0/24", "2001:db8:49::/48", "5012", "fd00:dc1:c:1394::"}}},
}

var logicalTenants = [2]struct{ class, title string }{
	{"t1", "Tenant 1 · one routed network · RT 65535:1001"},
	{"t2", "Tenant 2 · one routed network · RT 65535:1002"},
}

const logicalStyle = `<style>
  text { font-family: ui-sans-serif, -apple-system, "Segoe UI", Helvetica, Arial, sans-serif; fill: #1f2328; }
  .scene { font-size: 13px; font-weight: 600; }
  .part { fill: #f6f8fa; stroke: #d0d7de; }
  .plabel { font-size: 12px; font-weight: 600; fill: #59636e; }
  .ns { font-size: 10.5px; fill: #59636e; }
  .tt { font-size: 12px; font-weight: 600; }
  .net { stroke-width: 1.5; }
  .nl { font-size: 12px; font-weight: 600; }
  .mono { font-size: 10.5px; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
  .band { stroke-width: 6; stroke-linecap: round; }
  .sr { font-size: 10.5px; font-weight: 600; fill: #bc4c00; }
  .t1.net { fill: #ddf4ff; stroke: #0969da; } .t1.band { stroke: #54aeff; }
  .t2.net { fill: #dafbe1; stroke: #1a7f37; } .t2.band { stroke: #4ac26b; }
  @media (prefers-color-scheme: dark) {
    text { fill: #e6edf3; }
    .part { fill: #161b22; stroke: #3d444d; }
    .plabel, .ns { fill: #9198a1; }
    .sr { fill: #f0883e; }
    .t1.net { fill: #0c2d6b; stroke: #4493f8; } .t1.band { stroke: #1f6feb; }
    .t2.net { fill: #04260f; stroke: #3fb950; } .t2.band { stroke: #238636; }
  }
</style>`

func renderLogical() string {
	const w, h = 960, 380
	const netW, netH = 236, 62
	rows := [2]int{150, 262} // centre of each tenant's networks
	aria := "Logical view: three partitions, each with its own private network and VNI per tenant. Tenant 1 has " +
		"10.0.16.0/24 with VNI 3981 in A, 10.0.32.0/24 with VNI 4011 in B and 10.0.48.0/24 with VNI 5011 in C; tenant 2 " +
		"has 10.0.17.0/24 (VNI 3982), 10.0.33.0/24 (VNI 4012) and 10.0.49.0/24 (VNI 5012). The gateway pair of each " +
		"partition exports every tenant VRF with its own SRv6 SID; SRv6 L3VPN joins each tenant's networks into one " +
		"routed network (route targets 65535:1001 and 65535:1002). Tenants stay separate."

	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	p(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="%s">`, w, h, w, h, aria)
	p("%s", logicalStyle)
	p(`<text class="scene" x="480" y="26" text-anchor="middle">Logical view: per partition a private network and VNI per tenant, stitched by SRv6 L3VPN</text>`)
	for _, c := range logicalColumns {
		p(`<rect class="part" x="%d" y="44" width="304" height="282" rx="10"/><text class="plabel" x="%d" y="64" text-anchor="middle">%s · EVPN domain</text><text class="ns" x="%d" y="80" text-anchor="middle">%s</text>`,
			c.x-152, c.x, c.name, c.x, c.detail)
	}
	for i, t := range logicalTenants {
		y := rows[i]
		p(`<text class="tt" x="480" y="%d" text-anchor="middle">%s</text>`, y-netH/2-10, t.title)
		// the band joins all networks of the tenant; the networks sit on top
		p(`<line class="band %s" x1="%d" y1="%d" x2="%d" y2="%d"/>`, t.class, ca, y, cc, y)
		for _, x := range []int{(ca + cb) / 2, (cb + cc) / 2} {
			p(`<text class="sr" x="%d" y="%d" text-anchor="middle">SRv6</text>`, x, y-8)
		}
		for _, c := range logicalColumns {
			n := c.nets[i]
			p(`<rect class="net %s" x="%d" y="%d" width="%d" height="%d" rx="8"/>`, t.class, c.x-netW/2, y-netH/2, netW, netH)
			p(`<text class="nl" x="%d" y="%d" text-anchor="middle">%s · %s</text>`, c.x, y-12, n.v4, n.v6)
			p(`<text class="mono" x="%d" y="%d" text-anchor="middle">VNI %s · vrf%s</text>`, c.x, y+4, n.vni, n.vni)
			p(`<text class="mono" x="%d" y="%d" text-anchor="middle">SID %s</text>`, c.x, y+19, n.sid)
		}
	}
	p(`<text class="ns" x="480" y="350" text-anchor="middle">Inside a partition: EVPN type-5 with the partition's own VNI. Between partitions: VPNv4/v6 routes with one End.DT46 SID per VRF,</text>`)
	p(`<text class="ns" x="480" y="366" text-anchor="middle">SRv6 between the gateway pairs over an IPv6-only core. VNIs may differ; the route target says which networks belong together.</text>`)
	p("</svg>")
	return b.String()
}
