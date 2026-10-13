//go:build e2e

package lab

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Tenant 2 comes from the gateways' network source: the fake metal-apiserver
// on the core (configs/core/metal-api.yaml) lists the private networks of
// project tenant-2, one per partition. All other tests use tenant 2 like a
// static network; these check the source itself. Tests change the API's
// answer with an override file and simulate an outage with a "down" file
// (lab/cmd/fakeapi).

const (
	apiOverride = "/run/fakeapi/networks.yaml"
	apiDown     = "/run/fakeapi/down"
	tenant2RT   = "65535:3473928608" // derived from "project/tenant-2"
)

var allGateways = []string{"gw-a1", "gw-a2", "gw-b1", "gw-b2", "gw-c1", "gw-c2"}

// setAPI makes the fake metal-apiserver answer with the lab's networks plus
// extra ("" = the lab's networks only).
func setAPI(t *testing.T, extra string) {
	t.Helper()
	cmd := "rm -f " + apiOverride
	if extra != "" {
		cmd = "mkdir -p /run/fakeapi && cat /etc/fakeapi/networks.yaml - > " + apiOverride + ".tmp <<'EOF'\n" + extra + "EOF\nmv " + apiOverride + ".tmp " + apiOverride
	}
	if _, err := lab.Exec("core", "sh", "-c", cmd); err != nil {
		t.Fatal(err)
	}
}

// sourceStatus is the network source part of "open-dci status".
func sourceStatus(gw string) (networks map[string]string, err error) {
	out, err := lab.Exec(gw, "sh", "-c", "OPEN_DCI_OUTPUT=json /usr/local/bin/open-dci status -c /etc/open-dci/config.yaml || true")
	if err != nil {
		return nil, err
	}
	var st struct {
		Networks []struct{ VRF, Group, RouteTarget string }
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return nil, fmt.Errorf("%s: %v\n%s", gw, err, out)
	}
	networks = map[string]string{}
	for _, n := range st.Networks {
		if n.Group != "" {
			networks[n.VRF] = n.Group + " " + n.RouteTarget
		}
	}
	return networks, nil
}

// provisioned checks a source network's VRF on a gateway: kernel VRF and SID.
func provisioned(gw, vrf string, want bool) func() error {
	return func() error {
		_, err := lab.Exec(gw, "ip", "link", "show", vrf)
		sids, _ := lab.Vtysh(gw, "show segment-routing srv6 sid")
		has := err == nil && strings.Contains(sids, vrf)
		if has != want {
			return fmt.Errorf("%s: %s provisioned %v, want %v\n%s", gw, vrf, has, want, sids)
		}
		return nil
	}
}

func TestSourceStitchesTenant2(t *testing.T) {
	for _, m := range []machine{mA2, mB2, mC2} {
		for _, gw := range m.gws {
			t.Run(gw, func(t *testing.T) {
				waitFor(t, converge, func() error {
					nets, err := sourceStatus(gw)
					if err != nil {
						return err
					}
					if got, want := nets[m.vrf], "project/tenant-2 "+tenant2RT; got != want || len(nets) != 1 {
						return fmt.Errorf("%s: source networks %v, want %s: %s", gw, nets, m.vrf, want)
					}
					return nil
				})
				waitFor(t, converge, metricsContain(gw, "open_dci_source_networks 1", "open_dci_source_stale 0"))
			})
		}
	}
}

// A new project's networks in partitions A and B are stitched there, and only
// there; dropped from the answer, they are removed. A project with two
// networks in one partition is reported, not stitched, and a project that
// isn't listed isn't stitched even if its networks carry a label (the lab
// uses the project mode; label mode is exclusive, see the unit tests).
func TestSourceAddRemove(t *testing.T) {
	defer setAPI(t, "")
	setAPI(t, `  - {id: t3-a, project: tenant-3, partition: a, vrf: 3990, prefixes: [10.3.16.0/24]}
  - {id: t3-b, project: tenant-3, partition: b, vrf: 4020, prefixes: [10.3.32.0/24]}
  - {id: t4-a, project: tenant-4, partition: a, vrf: 3991, prefixes: [10.4.16.0/24]}
  - {id: t4-c1, project: tenant-4, partition: c, vrf: 5021, prefixes: [10.4.48.0/24]}
  - {id: t4-c2, project: tenant-4, partition: c, vrf: 5022, prefixes: [10.4.49.0/24]}
  - {id: t5-a, project: tenant-5, partition: a, vrf: 3992, prefixes: [10.5.16.0/24], labels: {open-dci.io/stitch: web}}
  - {id: t5-b, project: tenant-5, partition: b, vrf: 4022, prefixes: [10.5.32.0/24], labels: {open-dci.io/stitch: web}}
`)
	vrfs := map[string]string{"gw-a1": "vrf3990", "gw-a2": "vrf3990", "gw-b1": "vrf4020", "gw-b2": "vrf4020"}
	for _, gw := range allGateways {
		if vrf, ok := vrfs[gw]; ok {
			waitFor(t, converge, provisioned(gw, vrf, true))
			waitFor(t, converge, metricsContain(gw, "open_dci_source_networks 2"))
		} else {
			waitFor(t, converge, metricsContain(gw, "open_dci_source_networks 1"))
			for _, vrf := range []string{"vrf5021", "vrf5022", "vrf3992", "vrf4022"} {
				if err := provisioned(gw, vrf, false)(); err != nil {
					t.Error(err)
				}
			}
		}
	}
	for _, gw := range []string{"gw-a1", "gw-c1"} {
		waitFor(t, converge, func() error {
			out, err := scrape(gw)
			if err != nil {
				return err
			}
			if !strings.Contains(out, `open_dci_source_skipped{group="project/tenant-4",reason="2 networks in partition c`) {
				return fmt.Errorf("%s: project/tenant-4 not reported as skipped", gw)
			}
			return nil
		})
	}
	for gw, vrf := range map[string]string{"gw-a1": "vrf3991", "gw-a2": "vrf3992", "gw-b1": "vrf4022"} {
		if err := provisioned(gw, vrf, false)(); err != nil {
			t.Error(err)
		}
	}

	setAPI(t, "")
	for gw, vrf := range vrfs {
		waitFor(t, converge, provisioned(gw, vrf, false))
		waitFor(t, converge, func() error { // with its devices
			for _, dev := range []string{"dcibr" + vrf[3:], "dcivx" + vrf[3:]} {
				if _, err := lab.Exec(gw, "ip", "link", "show", dev); err == nil {
					return fmt.Errorf("%s: %s still exists", gw, dev)
				}
			}
			return nil
		})
	}
	waitFor(t, converge, func() error { return lab.Ping("m-a2", mB2.v4, 0) })
}

// While metal-apiserver is unreachable, the gateways keep the last answer:
// nothing is removed, the gateway stays healthy, the source is reported stale.
func TestSourceOutage(t *testing.T) {
	const gw = "gw-a1"
	defer lab.Exec("core", "rm", "-f", apiDown)
	if _, err := lab.Exec("core", "sh", "-c", "mkdir -p /run/fakeapi && touch "+apiDown); err != nil {
		t.Fatal(err)
	}
	waitFor(t, converge, metricsContain(gw, "open_dci_source_stale 1"))
	waitFor(t, converge, func() error {
		c, h, err := healthz(gw)
		switch {
		case err != nil:
			return err
		case c != 200 || !h.Healthy:
			return fmt.Errorf("%s: /healthz %d %+v during the outage", gw, c, h)
		case len(h.Warnings) == 0 || !strings.Contains(h.Warnings[0], "source: no answer since"):
			return fmt.Errorf("%s: no source warning: %+v", gw, h.Warnings)
		}
		return nil
	})
	if err := provisioned(gw, mA2.vrf, true)(); err != nil {
		t.Error(err)
	}
	if err := lab.Ping("m-a2", mB2.v4, 0); err != nil {
		t.Error(err)
	}
	out, _ := scrape(gw)
	if strings.Contains(out, "open_dci_source_errors_total 0\n") {
		t.Error("no source errors counted")
	}

	lab.Exec("core", "rm", "-f", apiDown)
	waitFor(t, converge, metricsContain(gw, "open_dci_source_stale 0"))
	time.Sleep(time.Second)
	waitFor(t, converge, func() error { return lab.Ping("m-a2", mB2.v4, 0) })
}
