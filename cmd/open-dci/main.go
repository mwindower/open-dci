// open-dci turns an FRR box attached to an EVPN fabric (a dedicated gateway,
// e.g. at the exit) into a gateway that stitches tenant VRFs across EVPN
// domains via SRv6 L3VPN.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mwindower/open-dci/internal/api"
	"github.com/mwindower/open-dci/internal/config"
	"github.com/mwindower/open-dci/internal/frr"
	"github.com/mwindower/open-dci/internal/gateway"
	"github.com/mwindower/open-dci/internal/metrics"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

const usage = `open-dci - stitch EVPN tenant VRFs across partitions via SRv6 L3VPN

Usage:
  open-dci validate -c FILE              check the config file
  open-dci render   -c FILE [--asn N --router-id IP]
                                         print the FRR configuration open-dci adds
  open-dci diff     -c FILE              show what is missing in the running FRR
  open-dci apply    -c FILE              reconcile kernel and FRR once
  open-dci run      -c FILE [-i 10s] [--listen ADDR] [--api-listen ADDR ...]
                                         reconcile continuously; --listen serves
                                         Prometheus metrics (/metrics) and the
                                         health (/healthz) on ADDR, e.g. :9469;
                                         --api-listen serves the gRPC API (networks
                                         set by dci-controller or others), e.g. :9470
  open-dci status   -c FILE [--routes all|VRF|VNI]
                                         show sessions, SIDs, prefixes and drift;
                                         --routes lists the networks' local and
                                         remote prefixes and EVPN type-5 routes
  open-dci drain    -c FILE              withdraw locator and routes (maintenance)
  open-dci undrain  -c FILE              announce again
  open-dci version

API flags (run):
  --api-listen ADDR        serve the API (gRPC, gRPC-Web, Connect) on ADDR
  --api-token-file FILE    bearer token every API call must carry (required)
  --api-tls-cert FILE      serve TLS with this certificate (default: plaintext h2c)
  --api-tls-key FILE       ... and key

Common flags:
  -c FILE               config file (default /etc/open-dci/config.yaml)
  --vtysh PATH          vtysh binary (default vtysh)
  --state FILE          last applied FRR snippet (default /var/lib/open-dci/applied.conf)
  --api-networks FILE   the networks set through the API (default: api-networks.json
                        next to --state); every command applies them if the file exists
  -v                    debug logging
`

func main() {
	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-") {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	if cmd == "version" {
		fmt.Println("open-dci", version)
		return
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	cfgPath := fs.String("c", "/etc/open-dci/config.yaml", "config file")
	vtysh := fs.String("vtysh", "vtysh", "vtysh binary")
	state := fs.String("state", "/var/lib/open-dci/applied.conf", "state file")
	interval := fs.Duration("i", 10*time.Second, "reconcile interval (run)")
	listen := fs.String("listen", "", "address for /metrics and /healthz, e.g. :9469 (run)")
	asn := fs.Uint("asn", 0, "BGP ASN (render without FRR)")
	routerID := fs.String("router-id", "", "BGP router-id (render without FRR)")
	routes := fs.String("routes", "", "list the routes of a network: VRF, VNI or all (status)")
	apiListen := fs.String("api-listen", "", "address for the API, e.g. :9470 (run)")
	apiToken := fs.String("api-token-file", "", "file with the API's bearer token (run)")
	apiCert := fs.String("api-tls-cert", "", "TLS certificate for the API (run)")
	apiKey := fs.String("api-tls-key", "", "TLS key for the API (run)")
	apiNetworks := fs.String("api-networks", "", "file of the API networks (default: next to --state)")
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
	// the API networks: every command applies them if they exist, so none of
	// them takes the API's VRFs for stale; run with the API enables them
	if *apiNetworks == "" {
		*apiNetworks = filepath.Join(filepath.Dir(*state), "api-networks.json")
	}
	serveAPI := cmd == "run" && *apiListen != ""
	if _, err := os.Stat(*apiNetworks); err == nil || serveAPI {
		store, err := gateway.OpenAPIStore(*apiNetworks)
		if err != nil {
			fatal(err)
		}
		gw.UseAPI(store)
	}
	if cmd == "run" && !serveAPI && len(gw.Config.Networks) == 0 {
		fatal(fmt.Errorf("no networks: list them in %s or enable the API (--api-listen)", *cfgPath))
	}

	switch cmd {
	case "validate":
		fmt.Printf("%s: valid (loopback %s, %d peer(s), %d network(s))\n", *cfgPath, cfg.Gateway.Loopback(), len(cfg.Peers), len(gw.Config.Networks))
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
		log.Info("open-dci started", "version", version, "config", *cfgPath, "interval", *interval, "loopback", cfg.Gateway.Loopback())
		if *listen != "" {
			gw.Monitor = &gateway.Monitor{}
			srv := &http.Server{Addr: *listen, Handler: metrics.Handler(gw.Monitor, version), ReadHeaderTimeout: 5 * time.Second}
			go func() {
				log.Info("serving /metrics and /healthz", "listen", *listen)
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					// the gateway works without it: log, don't stop
					log.Error("metrics listener", "err", err)
				}
			}()
			defer srv.Close()
		}
		if serveAPI {
			if gw.Monitor == nil {
				gw.Monitor = &gateway.Monitor{}
			}
			srv, err := apiServer(*apiListen, *apiToken, *apiCert, *apiKey, gw, log)
			if err != nil {
				fatal(err)
			}
			defer srv.Close()
		}
		gw.Run(ctx, *interval)
	case "status":
		err = status(os.Stdout, gw, *routes)
	case "drain", "undrain":
		if err = gw.SetDrained(cmd == "drain"); err == nil {
			_, err = gw.Reconcile()
		}
		if err == nil {
			fmt.Printf("%sed: %s\n", cmd, map[bool]string{
				true:  "locator and type-5 routes withdrawn; the partner takes over within seconds. A running \"open-dci run\" keeps the state.",
				false: "announcing again once the gateway is ready (see status)",
			}[cmd == "drain"])
		}
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
	if len(res.Missing) == 0 && res.Removed == "" && res.RemovedL3VNIs == "" {
		fmt.Fprintln(w, "in sync")
		return nil
	}
	for _, l := range res.Missing {
		fmt.Fprintf(w, "+ %s\n", strings.Join(append(append([]string{}, l.Context...), l.Text), " > "))
	}
	if res.Removed != "" || res.RemovedL3VNIs != "" {
		fmt.Fprintf(w, "stale lines to remove:\n%s%s", res.RemovedL3VNIs, res.Removed)
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// apiServer serves the API on addr in the background: gRPC over HTTP/2,
// plaintext (h2c) unless a TLS certificate is given.
func apiServer(addr, tokenFile, cert, key string, gw *gateway.Gateway, log *slog.Logger) (*http.Server, error) {
	if tokenFile == "" {
		return nil, fmt.Errorf("--api-listen needs --api-token-file")
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, fmt.Errorf("%s: empty token", tokenFile)
	}
	if (cert == "") != (key == "") {
		return nil, fmt.Errorf("--api-tls-cert and --api-tls-key go together")
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.Handler(&api.Server{Gateway: gw, Monitor: gw.Monitor, Version: version}, token),
		ReadHeaderTimeout: 5 * time.Second,
		Protocols:         new(http.Protocols),
	}
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetHTTP2(true)
	srv.Protocols.SetUnencryptedHTTP2(true)
	go func() {
		log.Info("serving the API", "listen", addr, "tls", cert != "")
		var err error
		if cert != "" {
			err = srv.ListenAndServeTLS(cert, key)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			// the gateway works without it, with the networks it has
			log.Error("api listener", "err", err)
		}
	}()
	return srv, nil
}
