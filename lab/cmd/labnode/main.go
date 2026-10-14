// labnode is the container entrypoint for lab nodes. It waits for containerlab
// to plumb the data interfaces, applies the node's declarative kernel setup
// (node.yaml) via netlink, starts sidecars (open-dci on the gateways) and
// then execs FRR.
//
// "labnode post URL [Header:value ...]" is a small HTTP client for the e2e
// tests (the FRR image has none that reports error bodies): it POSTs stdin
// and prints the status code, a newline and the response body.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/mwindower/open-dci/lab/internal/node"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "post" {
		if err := post(os.Args[2], os.Args[3:]); err != nil {
			fmt.Fprintln(os.Stderr, "post:", err)
			os.Exit(1)
		}
		return
	}
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

// post sends stdin to url with the given headers ("Name: value") and prints
// the status code and the body.
func post(url string, headers []string) error {
	req, err := http.NewRequest(http.MethodPost, url, os.Stdin)
	if err != nil {
		return err
	}
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ":")
		req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	fmt.Printf("%d\n%s", resp.StatusCode, body)
	return nil
}
