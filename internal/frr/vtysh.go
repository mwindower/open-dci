package frr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Vtysh talks to the local FRR via the vtysh binary.
type Vtysh struct {
	Path string // default "vtysh"
}

func (v Vtysh) bin() string {
	if v.Path != "" {
		return v.Path
	}
	return "vtysh"
}

func (v Vtysh) run(args ...string) (string, error) {
	cmd := exec.Command(v.bin(), args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	// vtysh reports config errors on stdout ("% ...") and does not always exit non-zero
	if err != nil || strings.Contains(out.String(), "\n% ") || strings.HasPrefix(out.String(), "% ") {
		return out.String(), fmt.Errorf("vtysh %s: %v: %s%s", strings.Join(args, " "), err, strings.TrimSpace(out.String()), strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Show runs a single show command.
func (v Vtysh) Show(cmd string) (string, error) { return v.run("-c", cmd) }

// ShowJSON runs a show command with "json" appended and decodes the result.
func (v Vtysh) ShowJSON(cmd string, out any) error {
	s, err := v.Show(cmd + " json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(s), out); err != nil {
		return fmt.Errorf("decode %q: %w", cmd, err)
	}
	return nil
}

// RunningConfig returns "show running-config".
func (v Vtysh) RunningConfig() (string, error) { return v.Show("show running-config") }

// Apply feeds configuration commands to FRR (incrementally, like a config
// file read at startup). It does not touch /etc/frr/frr.conf.
func (v Vtysh) Apply(cfg string) error {
	f, err := os.CreateTemp("", "srv6-dci-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(cfg); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = v.run("-f", f.Name())
	return err
}

var (
	reRouterBGP = regexp.MustCompile(`^router bgp (\d+)$`)
	reRouterVRF = regexp.MustCompile(`^router bgp (\d+) vrf (\S+)$`)
	reRouterID  = regexp.MustCompile(`^bgp router-id (\S+)$`)
)

// Base describes the existing (base) BGP configuration srv6-dci augments.
type Base struct {
	Identity
	VRFInstances map[string]bool // VRFs with a "router bgp <asn> vrf <name>" instance
}

// DiscoverBase extracts the default BGP instance's ASN and router-id and the
// per-VRF BGP instances from a running configuration.
func DiscoverBase(running string) (Base, error) {
	b := Base{VRFInstances: map[string]bool{}}
	for _, l := range Parse(running) {
		if len(l.Context) == 0 {
			if m := reRouterBGP.FindStringSubmatch(l.Text); m != nil {
				asn, _ := strconv.ParseUint(m[1], 10, 32)
				b.ASN = uint32(asn)
			}
			if m := reRouterVRF.FindStringSubmatch(l.Text); m != nil {
				b.VRFInstances[m[2]] = true
			}
			continue
		}
		if len(l.Context) == 1 && reRouterBGP.MatchString(l.Context[0]) {
			if m := reRouterID.FindStringSubmatch(l.Text); m != nil {
				b.RouterID = m[1]
			}
		}
	}
	if b.ASN == 0 {
		return b, fmt.Errorf("no default BGP instance (router bgp <asn>) in the running configuration")
	}
	return b, nil
}
