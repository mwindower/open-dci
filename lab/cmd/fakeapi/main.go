// fakeapi stands in for metal-apiserver in the lab: it serves the private
// networks of configs/core/metal-api.yaml (network List of the admin and the
// project API) to the gateways' network source. Tests change the answer by
// writing an override file and simulate an outage with a "down" file.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/mwindower/open-dci/internal/source/metalstack/fakeapi"
)

func main() {
	listen := flag.String("listen", ":8080", "listen address")
	file := flag.String("f", "/etc/fakeapi/networks.yaml", "networks")
	override := flag.String("override", "/run/fakeapi/networks.yaml", "served instead of -f while it exists")
	down := flag.String("down", "/run/fakeapi/down", "answer \"unavailable\" while it exists")
	flag.Parse()
	srv := &fakeapi.Server{File: *file, Override: *override, Down: *down}
	slog.Info("fake metal-apiserver", "listen", *listen, "networks", *file)
	s := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
	if err := s.ListenAndServe(); err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
}
