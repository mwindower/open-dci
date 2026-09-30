package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/mwindower/open-dci/internal/gateway"
)

// status prints the gateway state; with OPEN_DCI_OUTPUT=json as JSON.
// It exits non-zero (via the returned error) when the gateway is unhealthy.
func status(w io.Writer, gw *gateway.Gateway) error {
	st, err := gw.Status()
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
	if k.TransportVRF != "" {
		fmt.Fprintf(w, "transport vrf %s  veth up: %s  path MTU: %d (need %d)  local rule last: %s  vrf strict_mode: %s\n",
			k.TransportVRF, yes(k.VethUp), k.DCIPathMTU, k.RequiredMTU, yes(k.LocalRuleLast), k.StrictMode)
	} else {
		fmt.Fprintf(w, "transport default VRF  vrf strict_mode: %s\n", k.StrictMode)
	}
	if k.Err != "" {
		fmt.Fprintf(w, "          %s\n", k.Err)
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
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d %s\t%d/%d\t%d/%d\n", n.VRF, n.RouteTarget, n.RD, sid, n.VNI, n.L3VNIState, n.LocalV4, n.LocalV6, n.RemoteV4, n.RemoteV6)
	}
	tw.Flush()
}
