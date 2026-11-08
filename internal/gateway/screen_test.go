package gateway

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/mwindower/open-dci/internal/config"
	"github.com/mwindower/open-dci/internal/kernel"
)

func TestKernelConflict(t *testing.T) {
	n := config.Network{VRF: "vrf3981", VNI: 3981, Table: 3981}
	link := func(l netlink.Link, name, alias string) netlink.Link {
		*l.Attrs() = netlink.LinkAttrs{Name: name, Alias: alias}
		return l
	}
	for _, c := range []struct {
		name string
		link netlink.Link
		want string
	}{
		{"foreign vrf", link(&netlink.Vrf{Table: 7}, "vrf3981", ""), "vrf3981 exists but was not created by open-dci"},
		{"foreign device named like the vrf", link(&netlink.Dummy{}, "vrf3981", ""), "not created by open-dci"},
		{"foreign bridge", link(&netlink.Bridge{}, "dcibr3981", "other"), "dcibr3981 exists"},
		{"foreign vxlan with the vni", link(&netlink.Vxlan{VxlanId: 3981}, "vni3981", ""), "VNI 3981 is used by vni3981"},
		{"foreign vrf with the table", link(&netlink.Vrf{Table: 3981}, "vrf-base", ""), "table 3981 is used by vrf vrf-base"},
		{"own devices", link(&netlink.Vrf{Table: 3981}, "vrf3981", kernel.OwnerAlias), ""},
		{"unrelated", link(&netlink.Vxlan{VxlanId: 104100}, "vni104100", ""), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := kernelConflict(n, []netlink.Link{c.link}, false)
			if c.want == "" && got != "" || !strings.Contains(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			if got := kernelConflict(n, []netlink.Link{c.link}, true); got != "" {
				t.Fatalf("skipped kernel: got %q", got)
			}
		})
	}
}

const screenRunning = `router bgp 4200000016
 bgp router-id 10.0.0.16
exit
!
router bgp 4200000016 vrf vrf104100
exit
!
router bgp 4200000016 vrf vrf3981
exit
!
vrf vrf104100
 vni 104100
exit-vrf
!
vrf vrf3981
 vni 3981
exit-vrf
!
`

func TestFRRConflict(t *testing.T) {
	cfg := &config.Config{Networks: []config.Network{{VRF: "vrf3981", VNI: 3981}}}
	vnis := frrVNIs(screenRunning)
	if vnis[104100] != "vrf104100" || vnis[3981] != "vrf3981" || len(vnis) != 2 {
		t.Fatalf("vnis %v", vnis)
	}
	for _, c := range []struct {
		name string
		n    config.Network
		prev string
		want string
	}{
		{"vni of a base vrf", config.Network{VRF: "vrf9", VNI: 104100}, "", "VNI 104100 belongs to FRR vrf vrf104100"},
		{"own vrf keeps its vni", config.Network{VRF: "vrf3981", VNI: 3981}, "router bgp 4200000016 vrf vrf3981\n", ""},
		{"vni moving between own vrfs", config.Network{VRF: "vrf9", VNI: 3981}, "", ""},
		{"base bgp instance", config.Network{VRF: "vrf104100", VNI: 5}, "", "BGP instance for vrf vrf104100 that open-dci didn't add"},
		{"bgp instance open-dci added", config.Network{VRF: "vrf3981", VNI: 3981}, "router bgp 4200000016 vrf vrf3981\n", ""},
		{"new vrf", config.Network{VRF: "vrf7", VNI: 7}, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := frrConflict(c.n, cfg, vnis, screenRunning, c.prev, "4200000016", nil, true)
			if c.want == "" && got != "" || !strings.Contains(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
	// the state file is gone, but the kernel VRF is open-dci's
	own := &netlink.Vrf{LinkAttrs: netlink.LinkAttrs{Name: "vrf3981", Alias: kernel.OwnerAlias}}
	if got := frrConflict(config.Network{VRF: "vrf3981", VNI: 3981}, cfg, vnis, screenRunning, "", "4200000016", []netlink.Link{own}, false); got != "" {
		t.Fatalf("own vrf without state: %q", got)
	}
}

// screen sets conflicting API networks aside and lets one back in once its
// conflict is gone.
func TestScreen(t *testing.T) {
	static := &config.Config{Networks: []config.Network{{VRF: "vrf1", VNI: 1}}}
	wanted := &config.Config{Networks: append(slices.Clone(static.Networks),
		config.Network{VRF: "vrf9", VNI: 104100, Group: "p/bad"}, config.Network{VRF: "vrf7", VNI: 7})}
	g := &Gateway{Config: wanted, static: static, SkipKernel: true, Log: slog.New(slog.DiscardHandler),
		StateFile: filepath.Join(t.TempDir(), "state")}
	vrfs := func() (out []string) {
		for _, n := range g.Config.Networks {
			out = append(out, n.VRF)
		}
		return out
	}
	g.screen(screenRunning)
	if got := strings.Join(vrfs(), " "); got != "vrf1 vrf7" {
		t.Fatalf("screened: %s", got)
	}
	if b := g.Blocked(); len(b) != 1 || b[0].VRF != "vrf9" || b[0].Group != "p/bad" || !strings.Contains(b[0].Reason, "VNI 104100") {
		t.Fatalf("blocked: %+v", b)
	}
	// the next round screens the wanted config again, not the screened one
	g.screen(strings.Replace(screenRunning, " vni 104100\n", "", 1))
	if got := strings.Join(vrfs(), " "); got != "vrf1 vrf9 vrf7" || len(g.Blocked()) != 0 {
		t.Fatalf("after the conflict: %s, blocked %v", got, g.Blocked())
	}
	if g.Config != wanted {
		t.Fatal("without conflicts, Config is the wanted config")
	}
	// takeAPI doesn't take the unscreened config for a new one
	g.screen(screenRunning)
	g.latest = wanted
	g.takeAPI()
	if g.Config == wanted {
		t.Fatal("takeAPI replaced the screened config by the same wanted one")
	}
	if _, err := os.Stat(g.StateFile); err == nil {
		t.Fatal("screen must not write the state")
	}
}
