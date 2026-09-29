// srv6-dci turns a metal-stack firewall (or any FRR-based EVPN VTEP) into a
// gateway that stitches tenant VRFs across EVPN domains via SRv6 L3VPN.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mwindower/srv6-dci/internal/config"
	"github.com/mwindower/srv6-dci/internal/frr"
	"github.com/mwindower/srv6-dci/internal/gateway"
)

const usage = `srv6-dci - stitch EVPN tenant VRFs across partitions via SRv6 L3VPN

Usage:
  srv6-dci validate -c FILE              check the config file
  srv6-dci render   -c FILE [--asn N --router-id IP]
                                         print the FRR configuration srv6-dci adds
  srv6-dci diff     -c FILE              show what is missing in the running FRR
  srv6-dci apply    -c FILE              reconcile kernel and FRR once
  srv6-dci run      -c FILE [-i 10s]     reconcile continuously
  srv6-dci status   -c FILE              show sessions, SIDs, prefixes and drift

Common flags:
  -c FILE        config file (default /etc/srv6-dci/config.yaml)
  --vtysh PATH   vtysh binary (default vtysh)
  --state FILE   last applied FRR snippet (default /var/lib/srv6-dci/applied.conf)
  -v             debug logging
`

func main() {
	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-") {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	cfgPath := fs.String("c", "/etc/srv6-dci/config.yaml", "config file")
	vtysh := fs.String("vtysh", "vtysh", "vtysh binary")
	state := fs.String("state", "/var/lib/srv6-dci/applied.conf", "state file")
	interval := fs.Duration("i", 10*time.Second, "reconcile interval (run)")
	asn := fs.Uint("asn", 0, "BGP ASN (render without FRR)")
	routerID := fs.String("router-id", "", "BGP router-id (render without FRR)")
	verbose := fs.Bool("v", false, "debug logging")
	_ = fs.Parse(os.Args[2:])

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal(err)
	}
	gw := &gateway.Gateway{Config: cfg, FRR: frr.Vtysh{Path: *vtysh}, Log: log, StateFile: *state}

	switch cmd {
	case "validate":
		fmt.Printf("%s: valid (loopback %s, %d peer(s), %d network(s))\n", *cfgPath, cfg.Gateway.Loopback(), len(cfg.Peers), len(cfg.Networks))
	case "render":
		err = render(os.Stdout, gw, uint32(*asn), *routerID)
	case "diff":
		err = diff(os.Stdout, gw)
	case "apply":
		var res gateway.Result
		res, err = gw.Reconcile()
		if err == nil {
			fmt.Printf("applied=%v missing-before=%d removed-stale=%v\n", res.Applied, len(res.Missing), res.Removed != "")
		}
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		log.Info("srv6-dci started", "config", *cfgPath, "interval", *interval, "loopback", cfg.Gateway.Loopback())
		gw.Run(ctx, *interval)
	case "status":
		err = status(os.Stdout, gw)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fatal(err)
	}
}

func render(w io.Writer, gw *gateway.Gateway, asn uint32, routerID string) error {
	id := frr.Identity{ASN: asn, RouterID: routerID}
	if id.ASN == 0 {
		id.ASN = gw.Config.Gateway.ASN
	}
	if id.RouterID == "" {
		id.RouterID = gw.Config.Gateway.RouterID
	}
	if id.ASN == 0 || id.RouterID == "" {
		// fall back to the local FRR
		_, res, err := gw.Plan()
		if err != nil {
			return fmt.Errorf("%w (or pass --asn and --router-id)", err)
		}
		id = res.Identity
	}
	out, err := frr.Render(gw.Config, id)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(w, out)
	return err
}

func diff(w io.Writer, gw *gateway.Gateway) error {
	_, res, err := gw.Plan()
	if err != nil {
		return err
	}
	if len(res.Missing) == 0 && res.Removed == "" {
		fmt.Fprintln(w, "in sync")
		return nil
	}
	for _, l := range res.Missing {
		fmt.Fprintf(w, "+ %s\n", strings.Join(append(append([]string{}, l.Context...), l.Text), " > "))
	}
	if res.Removed != "" {
		fmt.Fprintf(w, "stale lines to remove:\n%s", res.Removed)
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
