package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mwindower/open-dci/internal/gateway"
)

// status prints the gateway state; with OPEN_DCI_OUTPUT=json as JSON.
// routes selects the networks whose routes are listed ("", "all", VRF or VNI).
// It exits non-zero (via the returned error) when the gateway is unhealthy.
func status(w io.Writer, gw *gateway.Gateway, routes string) error {
	st, err := gw.Status(routes)
	if err != nil {
		return err
	}
	if os.Getenv("OPEN_DCI_OUTPUT") == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(st); err != nil {
			return err
		}
	} else {
		printStatus(w, st)
	}
	if !st.Healthy() {
		return fmt.Errorf("gateway not healthy")
	}
	return nil
}

func printStatus(w io.Writer, st *gateway.Status) {
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "NO"
	}
	drift := "in sync"
	if st.MissingLines > 0 {
		drift = fmt.Sprintf("DRIFT (%d lines missing)", st.MissingLines)
	}
	k := st.Kernel
	fmt.Fprintf(w, "gateway   %s  AS %d  router-id %s  locator %s\n", st.Loopback, st.ASN, st.RouterID, st.Locator)
	fmt.Fprintf(w, "frr       %s\n", drift)
	if !st.Health.OK {
		fmt.Fprintf(w, "health    UNHEALTHY: %s (open-dci run withdraws the gateway)\n", st.Health.Reason)
	}
	if st.Drained {
		fmt.Fprintf(w, "locator   DRAINED: nothing announced, the partner carries the traffic (open-dci undrain)\n")
	} else if st.Announced {
		fmt.Fprintf(w, "locator   announced\n")
	} else {
		fmt.Fprintf(w, "locator   WITHHELD: %s\n", st.WithheldReason)
	}
	if k.TransportVRF != "" {
		fmt.Fprintf(w, "transport vrf %s  veth up: %s  path MTU: %d (need %d)  local rule last: %s  vrf strict_mode: %s\n",
			k.TransportVRF, yes(k.VethUp), k.DCIPathMTU, k.RequiredMTU, yes(k.LocalRuleLast), k.StrictMode)
	} else {
		fmt.Fprintf(w, "transport default VRF  vrf strict_mode: %s\n", k.StrictMode)
	}
	filter := "MISSING"
	if len(k.Filter) == k.FilterRequired {
		var f []string
		for _, c := range k.Filter {
			f = append(f, fmt.Sprintf("%s %d", c.Name, c.Packets))
		}
		filter = "dropped: " + strings.Join(f, ", ")
	}
	fmt.Fprintf(w, "filter    %s\n", filter)
	if k.Err != "" {
		fmt.Fprintf(w, "          %s\n", k.Err)
	}

	if src := st.Source; src != nil {
		synced := "never"
		if !src.Synced.IsZero() {
			synced = src.Synced.Format(time.RFC3339)
		}
		fmt.Fprintf(w, "source    %d network(s), answered %s, %d error(s), %d skipped\n", src.Networks, synced, src.Errors, len(src.Skipped))
		for _, warn := range st.Warnings(time.Now()) {
			fmt.Fprintf(w, "          %s\n", warn)
		}
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nPEER\tAS\tSTATE\tUP\tVPNv4 RCVD/SENT\tVPNv6 RCVD/SENT")
	for _, p := range st.Peers {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d/%d\t%d/%d\n", p.Address, p.ASN, p.State, p.Uptime, p.V4Accepted, p.V4Sent, p.V6Accepted, p.V6Sent)
	}
	fmt.Fprintln(tw, "\nVRF\tRT\tRD\tSID\tL3VNI\tLOCAL v4/v6\tREMOTE v4/v6")
	for _, n := range st.Networks {
		sid := n.SID
		if sid == "" {
			sid = "-"
		} else {
			sid += " (" + n.Behavior + ")"
		}
		vrf := n.VRF
		if n.Group != "" {
			vrf += " (" + n.Group + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d %s\t%d/%d\t%d/%d\n", vrf, n.RouteTarget, n.RD, sid, n.VNI, n.L3VNIState, n.LocalV4, n.LocalV6, n.RemoteV4, n.RemoteV6)
		if n.NoBestPath > 0 {
			fmt.Fprintf(tw, "  %s: %d prefix(es) WITHOUT BEST PATH (not installed)\n", n.VRF, n.NoBestPath)
		}
	}
	tw.Flush()
	for _, n := range st.Networks {
		if n.Routes != nil {
			printRoutes(w, n)
		}
	}
}

// printRoutes lists a network's routes: local and remote prefixes of the
// tenant VRF, then the L3VNI's EVPN type-5 routes, indented under the network.
func printRoutes(w io.Writer, n gateway.NetworkStatus) {
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "NO"
	}
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	r := n.Routes
	fmt.Fprintf(w, "\n%s  VNI %d  RT %s\n", n.VRF, n.VNI, n.RouteTarget)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	fmt.Fprintf(tw, "\n  LOCAL (%d)\tNEXT HOP\tAS PATH\tVPN\tKERNEL\n", len(r.Local))
	for _, l := range r.Local {
		nh, vpn := dash(l.NextHop), "-"
		if l.Aggregate {
			nh = "aggregate"
		}
		if l.Exported {
			vpn = "exported"
		}
		kern := yes(l.Installed)
		if l.NoBestPath {
			kern = "NO BEST PATH"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", l.Prefix, nh, dash(l.ASPath), vpn, kern)
	}

	fmt.Fprintf(tw, "\n  REMOTE (%d)\tSID\tAS PATH\tPATHS\tKERNEL\n", len(r.Remote))
	for _, rm := range r.Remote {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%s\n", rm.Prefix, dash(strings.Join(rm.SIDs, ",")), dash(rm.ASPath), rm.Paths, yes(rm.Installed))
	}

	fmt.Fprintf(tw, "\n  EVPN TYPE-5 (%d)\tRD\tNEXT HOP\tAS PATH\tRT\tPATHS\n", len(r.EVPN))
	for _, e := range r.EVPN {
		dir := "in  "
		if e.Advertised {
			dir = "out "
		}
		paths := fmt.Sprint(e.Paths)
		if e.NoBestPath {
			paths += " NO BEST PATH"
		}
		fmt.Fprintf(tw, "  %s%s\t%s\t%s\t%s\t%s\t%s\n", dir, e.Prefix, e.RD, dash(e.NextHop), dash(e.ASPath), strings.Join(e.RTs, ","), paths)
	}
	tw.Flush()
}
