// dci-controller reconciles metal-apiserver's private networks with the
// open-dci gateways: tenants label their networks (e.g.
// open-dci.io/stitch: prod), and the controller sets every gateway the
// stitched networks of its partition through the gateway API.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mwindower/open-dci/internal/controller"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	cfgPath := flag.String("c", "/etc/dci-controller/config.yaml", "config file")
	once := flag.Bool("once", false, "run one round and exit (non-zero if it failed)")
	showVersion := flag.Bool("version", false, "print the version")
	verbose := flag.Bool("v", false, "debug logging")
	flag.Parse()
	if *showVersion {
		fmt.Println("dci-controller", version)
		return
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := controller.Load(*cfgPath)
	if err != nil {
		fatal(err)
	}
	c, err := controller.New(cfg, nil, log)
	if err != nil {
		fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *once {
		if err := c.Round(ctx); err != nil {
			fatal(err)
		}
		for name, g := range c.State().Gateways {
			if g.LastError != "" {
				fatal(fmt.Errorf("gateway %s: %s", name, g.LastError))
			}
		}
		return
	}
	log.Info("dci-controller started", "version", version, "gateways", len(cfg.Gateways), "interval", cfg.Interval, "metal-apiserver", cfg.MetalStack.URL)
	if cfg.Listen != "" {
		srv := &http.Server{Addr: cfg.Listen, Handler: controller.Handler(c, version), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			log.Info("serving /metrics and /healthz", "listen", cfg.Listen)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("metrics listener", "err", err)
			}
		}()
		defer srv.Close()
	}
	c.Run(ctx)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
