// Package labtest provides helpers for end-to-end tests against a running
// containerlab lab: exec into nodes, query FRR via vtysh JSON and poll until
// the network has converged.
package labtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Lab addresses the nodes of one deployed containerlab topology.
type Lab struct {
	Prefix string // e.g. "clab-open-dci-spike"
}

// Exec runs a command inside node and returns stdout.
func (l Lab) Exec(node string, args ...string) (string, error) {
	cmd := exec.Command("docker", append([]string{"exec", l.Prefix + "-" + node}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s: %s: %w: %s", node, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Vtysh runs a single vtysh command on node.
func (l Lab) Vtysh(node, command string) (string, error) {
	return l.Exec(node, "vtysh", "-c", command)
}

// VtyshJSON runs a vtysh "... json" command and decodes the result into v.
func (l Lab) VtyshJSON(node, command string, v any) error {
	out, err := l.Vtysh(node, command+" json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return fmt.Errorf("%s: decode %q: %w", node, command, err)
	}
	return nil
}

// Ping sends count echo requests with the given payload size (0 = default).
// IPv6 is never fragmented in transit, so a full-size v6 echo proves that
// every hop has enough MTU headroom.
func (l Lab) Ping(node, dst string, size int) error {
	args := []string{"ping", "-c", "2", "-W", "1"}
	if strings.Contains(dst, ":") {
		args = append(args, "-6")
	}
	if size > 0 {
		args = append(args, "-s", fmt.Sprint(size))
	}
	_, err := l.Exec(node, append(args, dst)...)
	return err
}

// BGPEstablished returns nil if node has at least one BGP peer and all peers
// in all address families (and VRFs) are Established.
func (l Lab) BGPEstablished(node string) error {
	var vrfs map[string]map[string]json.RawMessage
	if err := l.VtyshJSON(node, "show bgp vrf all summary", &vrfs); err != nil {
		return err
	}
	n := 0
	for vrf, afs := range vrfs {
		for af, raw := range afs {
			var s struct {
				Peers map[string]struct {
					State string `json:"state"`
				} `json:"peers"`
			}
			if json.Unmarshal(raw, &s) != nil {
				continue
			}
			for peer, p := range s.Peers {
				n++
				if p.State != "Established" {
					return fmt.Errorf("%s: vrf %s %s peer %s is %s", node, vrf, af, peer, p.State)
				}
			}
		}
	}
	if n == 0 {
		return fmt.Errorf("%s: no BGP peers", node)
	}
	return nil
}

// KernelRoute returns the kernel route lines for dst in the given VRF
// ("" = main table), e.g. to assert "encap seg6".
func (l Lab) KernelRoute(node, vrf, dst string) (string, error) {
	args := []string{"ip"}
	if strings.Contains(dst, ":") {
		args = append(args, "-6")
	}
	args = append(args, "route", "show")
	if vrf != "" {
		args = append(args, "vrf", vrf)
	}
	return l.Exec(node, append(args, dst)...)
}

// slow is the duration from which Eventually logs a success.
const slow = 15 * time.Second

// Eventually retries fn until it succeeds or timeout expires, then fails t
// with the last error. A slow success is logged with the last error, which
// shows what converged late.
func Eventually(t *testing.T, timeout time.Duration, fn func() error) {
	t.Helper()
	start := time.Now()
	deadline := start.Add(timeout)
	var last error
	for {
		err := fn()
		if err == nil {
			if d := time.Since(start); d > slow {
				t.Logf("slow: %s until success; last error: %v", d.Round(time.Second), last)
			}
			return
		}
		last = err
		if time.Now().After(deadline) {
			t.Fatalf("not reached within %s: %v", timeout, err)
		}
		time.Sleep(time.Second)
	}
}

// Contains returns a func for Eventually that checks that get() contains want.
func Contains(get func() (string, error), want string) func() error {
	return func() error {
		got, err := get()
		if err != nil {
			return err
		}
		if !strings.Contains(got, want) {
			return fmt.Errorf("output does not contain %q:\n%s", want, got)
		}
		return nil
	}
}
