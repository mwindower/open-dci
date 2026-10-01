//go:build e2e

package lab

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Filtering at the edge of the SRv6 domain. End.DT46 decapsulates whatever
// is addressed to a SID, whatever the source, so three places keep forged
// SRv6 packets away from the SIDs:
//   - the exits: nothing from the fabric side enters the locator block, and
//     from the core only with a source inside it (lab: edgeFilter in node.yaml)
//   - each gateway: its locator and loopback only accept sources inside the
//     block (open-dci's ingress filter)
//   - each gateway: tenants never reach the block (open-dci's ingress filter)
//
// Each test forges packets that would be decapsulated into a tenant VRF,
// checks that the responsible filter counted them and that the victim
// machine saw nothing.

const block = "fd00:dc1::/32"

func ensureTool(t *testing.T, node, bin, pkg string) {
	t.Helper()
	if _, err := lab.Exec(node, "sh", "-c", "command -v "+bin+" >/dev/null || apk add -q "+pkg); err != nil {
		t.Skipf("%s: cannot install %s: %v", node, pkg, err)
	}
}

// linkLocal returns the link-local address of dev on node.
func linkLocal(t *testing.T, node, dev string) string {
	t.Helper()
	out, err := lab.Exec(node, "ip", "-6", "-o", "addr", "show", "dev", dev, "scope", "link")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`inet6 (fe80::[0-9a-f:]+)/`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("%s %s: no link-local address:\n%s", node, dev, out)
	}
	return m[1]
}

// gatewayDrops returns a gateway's ingress-filter counter.
func gatewayDrops(t *testing.T, gw, rule string) uint64 {
	t.Helper()
	cmd := fmt.Sprintf("OPEN_DCI_OUTPUT=json /usr/local/bin/open-dci status -c /etc/open-dci/config.yaml")
	out, _ := lab.Exec(gw, "sh", "-c", cmd) // unhealthy exits non-zero, JSON is printed anyway
	var st struct {
		Kernel struct {
			Filter []struct {
				Name    string
				Packets uint64
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("%s: status: %v\n%s", gw, err, out)
	}
	for _, f := range st.Kernel.Filter {
		if f.Name == rule {
			return f.Packets
		}
	}
	t.Fatalf("%s: no filter rule %s", gw, rule)
	return 0
}

// exitDrops returns the counter of the lab edge filter rule matching text.
func exitDrops(t *testing.T, node, match string) uint64 {
	t.Helper()
	out, err := lab.Exec(node, "nft", "list", "table", "ip6", "lab-edge")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, match) {
			if m := regexp.MustCompile(`counter packets (\d+)`).FindStringSubmatch(l); m != nil {
				n, _ := strconv.ParseUint(m[1], 10, 64)
				return n
			}
		}
	}
	t.Fatalf("%s: no edge rule matching %q:\n%s", node, match, out)
	return 0
}

// captureICMP starts a capture on the victim and returns a func that waits
// for it and returns what it saw.
func captureICMP(t *testing.T, node, from string) func() string {
	t.Helper()
	ensureTool(t, node, "tcpdump", "tcpdump")
	f, err := os.CreateTemp(t.TempDir(), "cap")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		out, _ := lab.Exec(node, "timeout", "6", "tcpdump", "-nni", "lan0", "-c", "1", "icmp and src "+from)
		f.WriteString(out)
		close(done)
	}()
	time.Sleep(1500 * time.Millisecond) // tcpdump is listening
	return func() string {
		<-done
		b, _ := os.ReadFile(f.Name())
		return strings.TrimSpace(string(b))
	}
}

// An untrusted device on partition B's underlay (spine-b stands in for e.g. a
// tenant firewall) forges SRv6 to tenant 1's SID with a source spoofed inside
// the block. The gateway couldn't tell it from a remote gateway's; exit-b
// drops it at the edge.
func TestEdgeDropsForgedSRv6FromFabric(t *testing.T) {
	ensureTool(t, "exit-b", "nft", "nftables")
	before := exitDrops(t, "exit-b", `iifname "swp1*" ip6 daddr `+block)
	via := linkLocal(t, "exit-b", "swp1")
	// the outer packet is routed by its destination (the SID): spine-b needs
	// a route towards it, like any device that can reach the locators
	if _, err := lab.Exec("spine-b", "sh", "-c", fmt.Sprintf(
		"ip sr tunsrc set fd00:dc1:ff::a1 && ip -6 route replace %s/128 via %s dev swp2 && ip route replace %s/32 encap seg6 mode encap segs %s via inet6 %s dev swp2",
		mB.sid, via, mB.v4, mB.sid, via)); err != nil {
		t.Fatal(err)
	}
	defer lab.Exec("spine-b", "sh", "-c", "ip route del "+mB.v4+"/32; ip -6 route del "+mB.sid+"/128; ip sr tunsrc set ::")

	seen := captureICMP(t, "m-b", "10.0.1.13")
	lab.Exec("spine-b", "ping", "-c", "3", "-W", "1", mB.v4)
	if s := seen(); strings.Contains(s, "ICMP") {
		t.Fatalf("forged packet reached m-b:\n%s", s)
	}
	if after := exitDrops(t, "exit-b", `iifname "swp1*" ip6 daddr `+block); after < before+3 {
		t.Fatalf("exit-b edge filter counted %d, want +3", after-before)
	}
}

// Inside the domain, but with a source outside the block (here: exit-b
// itself, whose own packets bypass its edge filter): the gateway drops SRv6
// to its locator.
func TestGatewayDropsSRv6FromOutsideBlock(t *testing.T) {
	const gw = "gw-b1"
	before := gatewayDrops(t, gw, "locator-from-outside")
	via := linkLocal(t, gw, "uplink0")
	// pin the SID to gw-b1 (exit-b normally spreads it over the pair)
	if _, err := lab.Exec("exit-b", "sh", "-c", fmt.Sprintf(
		"ip sr tunsrc set 2001:db8:bad::1 && ip -6 route replace %s/128 via %s dev swp3 && ip route replace %s/32 encap seg6 mode encap segs %s via inet6 %s dev swp3",
		mB.sid, via, mB.v4, mB.sid, via)); err != nil {
		t.Fatal(err)
	}
	defer lab.Exec("exit-b", "sh", "-c", "ip route del "+mB.v4+"/32; ip -6 route del "+mB.sid+"/128; ip sr tunsrc set ::")

	seen := captureICMP(t, "m-b", "10.0.1.14")
	lab.Exec("exit-b", "ping", "-c", "3", "-W", "1", mB.v4)
	if s := seen(); strings.Contains(s, "ICMP") {
		t.Fatalf("forged packet reached m-b:\n%s", s)
	}
	if after := gatewayDrops(t, gw, "locator-from-outside"); after < before+3 {
		t.Fatalf("%s counted %d, want +3", gw, after-before)
	}
}

// A misconfigured fabric steers tenant 1 traffic for the locator block to
// gw-a1 (a route on leaf-a). m-a forges SRv6 to tenant 2's SID in partition
// B; without the filter, gw-b would decapsulate it into tenant 2's VRF.
func TestGatewayDropsTenantToTransport(t *testing.T) {
	const gw = "gw-a1"
	before := gatewayDrops(t, gw, "tenant-to-transport")
	if _, err := lab.Exec("leaf-a", "ip", "-6", "route", "replace", block, "vrf", mA.vrf, "via", "::ffff:10.0.0.16", "dev", "vlan3981", "onlink"); err != nil {
		t.Fatal(err)
	}
	defer lab.Exec("leaf-a", "ip", "-6", "route", "del", block, "vrf", mA.vrf)
	via := linkLocal(t, "leaf-a", "swp1")
	if _, err := lab.Exec("m-a", "sh", "-c", fmt.Sprintf(
		"ip -6 route replace %s via %s dev lan0 && ip route replace %s/32 encap seg6 mode encap segs %s via inet6 %s dev lan0", block, via, mB2.v4, mB2.sid, via)); err != nil {
		t.Fatal(err)
	}
	defer lab.Exec("m-a", "sh", "-c", "ip route del "+mB2.v4+"/32; ip -6 route del "+block)

	seen := captureICMP(t, "m-b2", mA.v4)
	lab.Exec("m-a", "ping", "-c", "3", "-W", "1", mB2.v4)
	if s := seen(); strings.Contains(s, "ICMP") {
		t.Fatalf("tenant 1 reached tenant 2 via a forged SID:\n%s", s)
	}
	if after := gatewayDrops(t, gw, "tenant-to-transport"); after < before+3 {
		t.Fatalf("%s counted %d, want +3", gw, after-before)
	}
	// m-a's regular traffic still works
	waitFor(t, converge, func() error { return lab.Ping("m-a", mB.v4, 0) })
}
