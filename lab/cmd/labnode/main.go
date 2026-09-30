// labnode is the container entrypoint for lab nodes. It waits for containerlab
// to plumb the data interfaces, applies the node's declarative kernel setup
// (node.yaml) via netlink, starts sidecars (open-dci on the firewalls) and
// then execs FRR.
package main

import (
	"flag"
	"log/slog"
	"os"
	"os/exec"
	"syscall"

	"github.com/mwindower/open-dci/lab/internal/node"
)

func main() {
	cfg := flag.String("config", "/etc/labnode/node.yaml", "node spec")
	check := flag.Bool("check", false, "only validate the spec and exit")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	spec, err := node.Load(*cfg)
	if err != nil {
		fail(log, "load spec", err)
	}
	if *check {
		log.Info("spec valid", "config", *cfg)
		return
	}
	if err := node.Apply(spec, log); err != nil {
		fail(log, "apply", err)
	}
	log.Info("kernel setup done")

	for _, c := range spec.Sidecars {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			fail(log, "sidecar "+c[0], err)
		}
		// it survives the exec below; tini (pid 1 afterwards) reaps it
		log.Info("sidecar started", "cmd", c, "pid", cmd.Process.Pid)
	}
	// Without vtysh.conf every vtysh call prints a misleading
	// "frr.conf processing failure: 11".
	if err := os.WriteFile("/etc/frr/vtysh.conf", []byte("service integrated-vtysh-config\n"), 0o644); err != nil {
		fail(log, "vtysh.conf", err)
	}
	argv := []string{"/sbin/tini", "--", "/usr/lib/frr/docker-start"}
	if err := syscall.Exec(argv[0], argv, os.Environ()); err != nil {
		fail(log, "exec frr", err)
	}
}

func fail(log *slog.Logger, msg string, err error) {
	log.Error(msg, "err", err)
	os.Exit(1)
}
