package node

import (
	"path/filepath"
	"strings"
	"testing"
)

// Every node.yaml of the lab must load and validate.
func TestLabSpecsValid(t *testing.T) {
	files, err := filepath.Glob("../../configs/*/node.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no node.yaml files found")
	}
	for _, f := range files {
		if _, err := Load(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestValidate(t *testing.T) {
	base := func() Spec {
		return Spec{
			VRFs:   []VRF{{Name: "vrf1", Table: 1000}},
			Bridge: &Bridge{Name: "bridge"},
			VNIs:   []VNI{{VNI: 1, VLAN: 1000, VRF: "vrf1", Local: "10.0.0.1"}},
		}
	}
	for _, c := range []struct {
		name    string
		mutate  func(*Spec)
		wantErr string
	}{
		{"valid", func(*Spec) {}, ""},
		{"duplicate table", func(s *Spec) { s.VRFs = append(s.VRFs, VRF{Name: "vrf2", Table: 1000}) }, "duplicate vrf"},
		{"vni without bridge", func(s *Spec) { s.Bridge = nil }, "require a bridge"},
		{"vni unknown vrf", func(s *Spec) { s.VNIs[0].VRF = "nope" }, "unknown vrf"},
		{"bad vtep", func(s *Spec) { s.VNIs[0].Local = "x" }, "local"},
		{"empty sidecar", func(s *Spec) { s.Sidecars = [][]string{{}} }, "empty sidecar"},
		{"bad loopback", func(s *Spec) { s.Loopback = []string{"10.0.0.1"} }, "no '/'"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := base()
			c.mutate(&s)
			err := s.Validate()
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}
