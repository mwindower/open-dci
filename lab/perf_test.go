//go:build e2e && perf

package lab

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Failure semantics under load (make lab-perf, not part of lab-check): a
// gateway or an exit of partition B fails while pings (every 20 ms) and TCP
// streams (iperf3) run between the partitions, and comes back later. The test
// reports how long each flow lost packets on failure and on recovery and how
// long the TCP streams stalled. It fails only if a TCP connection breaks or a
// flow doesn't recover.
//
// The failed exit is the one spine-b uses towards the gateways: then every
// direction of every flow crosses it (the gateways and the core spread over
// both exits anyway).
//
// "link": all links of the node go down (the neighbours see it at once).
// "hung": the node silently drops everything, links stay up (the neighbours
// notice when the BGP hold timer expires).
// "drained": planned maintenance, open-dci drain before the links go down.
// "gray": the gateway can't forward although BGP is fine; open-dci withdraws it.

const (
	perfInterval = 20 * time.Millisecond
	perfDuration = 45 * time.Second
	perfFailAt   = 10 * time.Second
	perfRestore  = 27 * time.Second
	perfStreams  = 8
)

// odci runs an open-dci command on a gateway (inside sh -c)
func odci(cmd string) string {
	return "/usr/local/bin/open-dci " + cmd + " -c /etc/open-dci/config.yaml"
}

const hang = "nft add table inet perf-hang && " +
	"nft add chain inet perf-hang pre '{ type filter hook prerouting priority -500; policy drop; }' && " +
	"nft add chain inet perf-hang out '{ type filter hook output priority -500; policy drop; }'"

var perfPings = [][2]string{
	{"m-a", mB.v4}, {"m-c", mB.v4}, {"m-b", mA.v4}, {"m-b", mC.v4}, {"m-a2", mB2.v4}, {"m-b2", mA2.v4},
}

func TestPerfFailover(t *testing.T) {
	for _, n := range []string{"m-a", "m-b", "m-c", "m-a2", "m-b2"} {
		ensureTool(t, n, "iperf3", "iperf3")
	}
	exit := fabricExit(t)
	for _, n := range []string{"gw-b2", exit} {
		ensureTool(t, n, "nft", "nftables")
	}
	exitLinks := "for i in swp1 swp2 swp3 swp4; do ip link set $i %s; done"
	for _, c := range []struct {
		name, node, fail, restore string
	}{
		{"gateway-link", "gw-b2", "ip link set uplink0 down; ip link set uplink1 down", "ip link set uplink0 up; ip link set uplink1 up"},
		{"gateway-hung", "gw-b2", hang, "nft delete table inet perf-hang"},
		// planned maintenance: drain, then stop; after the return, undrain once ready
		{"gateway-drained", "gw-b2", odci("drain") + " && sleep 3 && ip link set uplink0 down && ip link set uplink1 down",
			"ip link set uplink0 up; ip link set uplink1 up; for i in $(seq 60); do OPEN_DCI_OUTPUT=json " + odci("status") +
				" | grep -q '\"Announced\": true' && break; sleep 0.5; done; " + odci("undrain")},
		// gray failure: BGP fine, but no remote locator reachable (open-dci withdraws)
		{"gateway-gray", "gw-b2", "ip -6 route add unreachable fd00:dc1:a::/48 metric 1; ip -6 route add unreachable fd00:dc1:c::/48 metric 1",
			"ip -6 route del unreachable fd00:dc1:a::/48 metric 1; ip -6 route del unreachable fd00:dc1:c::/48 metric 1"},
		{"exit-link", exit, fmt.Sprintf(exitLinks, "down"), fmt.Sprintf(exitLinks, "up")},
		{"exit-hung", exit, hang, "nft delete table inet perf-hang"},
	} {
		t.Run(c.name, func(t *testing.T) {
			waitForHealthyLab(t)
			lab.Exec("m-b", "sh", "-c", "pkill iperf3; iperf3 -s -D -p 5201 && iperf3 -s -D -p 5202")
			defer lab.Exec("m-b", "pkill", "iperf3")
			time.Sleep(time.Second)

			var wg sync.WaitGroup
			pings := make([]string, len(perfPings))
			for i, p := range perfPings {
				wg.Add(1)
				go func() {
					defer wg.Done()
					n := strconv.Itoa(int(perfDuration / perfInterval))
					pings[i], _ = lab.Exec(p[0], "ping", "-i", fmt.Sprint(perfInterval.Seconds()), "-c", n, "-W", "1", p[1])
				}()
			}
			tcp := map[string]string{}
			var mu sync.Mutex
			for port, src := range map[string]string{"5201": "m-a", "5202": "m-c"} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					out, _ := lab.Exec(src, "iperf3", "-c", mB.v4, "-p", port, "-P", strconv.Itoa(perfStreams),
						"-t", strconv.Itoa(int((perfDuration - 2*time.Second).Seconds())), "-i", "0.2", "-b", "20M", "--json")
					mu.Lock()
					tcp[src] = out
					mu.Unlock()
				}()
			}
			restored := false
			restore := func() {
				if !restored {
					lab.Exec(c.node, "sh", "-c", c.restore)
					restored = true
				}
			}
			defer restore()
			time.Sleep(perfFailAt)
			if out, err := lab.Exec(c.node, "sh", "-c", c.fail); err != nil {
				t.Fatalf("fail %s: %v\n%s", c.node, err, out)
			}
			time.Sleep(perfRestore - perfFailAt)
			restore()
			wg.Wait()

			t.Logf("%s fails at %s, returns at %s", c.node, perfFailAt, perfRestore)
			for i, p := range perfPings {
				onFail, onRestore, err := pingLoss(pings[i])
				if err != nil {
					t.Errorf("ping %s → %s: %v", p[0], p[1], err)
				}
				t.Logf("ping %-4s → %-10s lost on failure %5.2fs, on recovery %5.2fs", p[0], p[1], onFail.Seconds(), onRestore.Seconds())
			}
			for _, src := range []string{"m-a", "m-c"} {
				stall, err := tcpStall(tcp[src])
				if err != nil {
					t.Errorf("TCP %s → %s: %v", src, mB.v4, err)
					continue
				}
				t.Logf("TCP  %-4s → %-10s %d streams survived, longest stall %.1fs", src, mB.v4, perfStreams, stall.Seconds())
			}
		})
	}
}

// fabricExit returns the exit spine-b forwards leaf-b's VXLAN traffic for
// gw-b2 through (ECMP picks one per VTEP pair).
func fabricExit(t *testing.T) string {
	t.Helper()
	out, err := lab.Exec("spine-b", "ip", "route", "get", "10.0.1.17", "from", "10.0.1.11", "iif", "swp1")
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case strings.Contains(out, "dev swp2 "):
		return "exit-b1"
	case strings.Contains(out, "dev swp3 "):
		return "exit-b2"
	}
	t.Fatalf("spine-b: no route to gw-b2 via an exit:\n%s", out)
	return ""
}

func waitForHealthyLab(t *testing.T) {
	t.Helper()
	for _, gw := range append(append(append([]string{}, pairA...), pairB...), pairC...) {
		waitFor(t, converge, func() error { return opendci(gw, "status") })
	}
	for _, p := range perfPings {
		waitFor(t, converge, func() error { return lab.Ping(p[0], p[1], 0) })
	}
}

// pingLoss returns the longest run of lost replies around the failure and
// around the recovery; a flow still down at the end is an error.
func pingLoss(out string) (onFail, onRestore time.Duration, err error) {
	got := map[int]bool{}
	for _, m := range regexp.MustCompile(`seq=(\d+)`).FindAllStringSubmatch(out, -1) {
		n, _ := strconv.Atoi(m[1])
		got[n] = true
	}
	total := int(perfDuration / perfInterval)
	if len(got) == 0 {
		return 0, 0, fmt.Errorf("no replies at all:\n%s", out)
	}
	if !got[total-1] && !got[total-2] {
		err = fmt.Errorf("not recovered at the end")
	}
	mid := perfFailAt + (perfRestore-perfFailAt)/2
	for i := 0; i < total; {
		if got[i] {
			i++
			continue
		}
		j := i
		for j < total && !got[j] {
			j++
		}
		d := time.Duration(j-i) * perfInterval
		if time.Duration(i)*perfInterval < mid {
			onFail = max(onFail, d)
		} else {
			onRestore = max(onRestore, d)
		}
		i = j
	}
	return onFail, onRestore, err
}

// tcpStall returns the longest time any stream moved less than 1 Mbit/s.
func tcpStall(out string) (time.Duration, error) {
	var r struct {
		Error     string
		Intervals []struct {
			Streams []struct {
				Socket int
				Start  float64
				End    float64
				Bps    float64 `json:"bits_per_second"`
			}
		}
		End struct {
			Received struct{ Bytes int64 } `json:"sum_received"`
		}
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return 0, fmt.Errorf("iperf3: %v\n%s", err, out)
	}
	if r.Error != "" || r.End.Received.Bytes == 0 {
		return 0, fmt.Errorf("iperf3: connection broke: %s", strings.TrimSpace(r.Error))
	}
	var longest float64
	stalled := map[int]float64{} // socket → start of the current stall
	for _, iv := range r.Intervals {
		for _, s := range iv.Streams {
			if s.Bps < 1e6 {
				if _, ok := stalled[s.Socket]; !ok {
					stalled[s.Socket] = s.Start
				}
				longest = max(longest, s.End-stalled[s.Socket])
			} else {
				delete(stalled, s.Socket)
			}
		}
	}
	return time.Duration(longest * float64(time.Second)), nil
}
