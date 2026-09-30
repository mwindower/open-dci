// Package frr renders the configuration open-dci adds to an existing FRR
// setup, compares it with FRR's running configuration, applies it via vtysh
// and reads operational state.
package frr

import (
	"bufio"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Line is one configuration command together with the context (the chain of
// block headers such as "router bgp 65000" / "address-family ipv4 vpn") it
// lives in.
type Line struct {
	Context []string
	Text    string
	// Leaf is false for block headers, i.e. lines that have nested commands.
	Leaf bool
}

// Key identifies a line independent of formatting.
func (l Line) Key() string {
	return strings.Join(append(append([]string{}, l.Context...), l.Text), " \x00 ")
}

// Parse splits an FRR configuration (as written by "show running-config" or
// rendered by open-dci) into lines with their context. Nesting is taken from
// the indentation, which FRR emits consistently. Separators ("!"), "exit*",
// "end" and preamble lines are dropped.
func Parse(cfg string) []Line {
	type frame struct {
		indent int
		text   string
		line   int // index into out
	}
	var (
		stack []frame
		out   []Line
	)
	sc := bufio.NewScanner(strings.NewReader(cfg))
	for sc.Scan() {
		raw := strings.TrimRight(sc.Text(), " \t")
		text := strings.TrimSpace(raw)
		if skip(text) {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			out[stack[len(stack)-1].line].Leaf = false // the parent has children
		}
		ctx := make([]string, len(stack))
		for i, f := range stack {
			ctx[i] = f.text
		}
		out = append(out, Line{Context: ctx, Text: text, Leaf: true})
		stack = append(stack, frame{indent, text, len(out) - 1})
	}
	return out
}

func skip(text string) bool {
	switch {
	case text == "", text == "!", text == "end",
		strings.HasPrefix(text, "exit"),
		strings.HasPrefix(text, "Building configuration"),
		strings.HasPrefix(text, "Current configuration"),
		strings.HasPrefix(text, "frr version"):
		return true
	}
	return false
}

// Missing returns the lines of want that are not present in have.
func Missing(want, have []Line) []Line {
	idx := make(map[string]bool, len(have))
	for _, l := range have {
		idx[l.Key()] = true
	}
	var out []Line
	for _, l := range want {
		if !idx[l.Key()] {
			out = append(out, l)
		}
	}
	return out
}

// ProvisionedVRFs returns the VRFs (name -> "vni N" line) whose L3VNI a
// rendered open-dci snippet provisions: the "vrf X" blocks with a "vni" line
// (the transport VRF's block never has one).
func ProvisionedVRFs(lines []Line) map[string]string {
	out := map[string]string{}
	for _, l := range lines {
		if len(l.Context) == 1 && strings.HasPrefix(l.Text, "vni ") {
			if vrf, ok := strings.CutPrefix(l.Context[0], "vrf "); ok {
				out[vrf] = l.Text
			}
		}
	}
	return out
}

// Removals returns the commands that undo leaf lines which were applied
// before (prev) but are no longer desired (want), rendered with their
// contexts. Block headers are never removed: they may belong to the base
// configuration (e.g. the transport VRF's "router bgp X vrf Y").
//
// The exception are VRFs open-dci provisioned itself and no longer wants
// (see ProvisionedVRFs). Their BGP instance is removed as a whole. FRR only
// accepts that once zebra has released the L3VNI, which happens
// asynchronously after L3VNIRemovals was applied; the FRR VRF itself can only
// be deleted once the kernel VRF is gone (see RemoveVRF).
func Removals(prev, want []Line) string {
	idx := make(map[string]bool, len(want))
	for _, l := range want {
		idx[l.Key()] = true
	}
	var (
		b       strings.Builder
		removed = map[string]bool{} // neighbors deleted via "no neighbor X remote-as"
		dropped = map[string]bool{} // top-level blocks removed as a whole
	)
	for vrf := range sortedMap(droppedVRFs(prev, want)) {
		dropped["vrf "+vrf] = true
		for _, l := range prev {
			if len(l.Context) == 0 && strings.HasPrefix(l.Text, "router bgp ") && strings.HasSuffix(l.Text, " vrf "+vrf) {
				fmt.Fprintf(&b, "no %s\n", l.Text)
				dropped[l.Text] = true
			}
		}
	}
	for _, l := range prev {
		if !l.Leaf || idx[l.Key()] {
			continue
		}
		if len(l.Context) > 0 && dropped[l.Context[0]] {
			continue
		}
		if n := neighborOf(l.Text); n != "" && removed[n] {
			continue // the whole neighbor is gone already
		}
		if n := neighborOf(l.Text); n != "" && strings.Contains(l.Text, " remote-as ") {
			removed[n] = true
		}
		for i, c := range l.Context {
			b.WriteString(strings.Repeat(" ", i) + c + "\n")
		}
		cmd := "no " + l.Text
		if rest, ok := strings.CutPrefix(l.Text, "no "); ok {
			cmd = rest
		}
		b.WriteString(strings.Repeat(" ", len(l.Context)) + cmd + "\n")
		for i := len(l.Context) - 1; i >= 0; i-- {
			b.WriteString(strings.Repeat(" ", i) + exitFor(l.Context[i]) + "\n")
		}
	}
	return b.String()
}

// L3VNIRemovals returns the commands that release the L3VNIs of provisioned
// VRFs that are no longer wanted. They must be applied before Removals.
func L3VNIRemovals(prev, want []Line) string {
	var b strings.Builder
	for vrf, vni := range sortedMap(droppedVRFs(prev, want)) {
		fmt.Fprintf(&b, "vrf %s\n no %s\nexit-vrf\n", vrf, vni)
	}
	return b.String()
}

// droppedVRFs returns the provisioned VRFs (name -> "vni N") of prev that
// want no longer provisions.
func droppedVRFs(prev, want []Line) map[string]string {
	out := ProvisionedVRFs(prev)
	for vrf := range ProvisionedVRFs(want) {
		delete(out, vrf)
	}
	return out
}

// RemoveVRF returns the command that deletes an FRR VRF. FRR refuses it
// while the kernel VRF exists, so it is applied after the VRF was deleted.
func RemoveVRF(vrf string) string { return "no vrf " + vrf + "\n" }

// sortedMap returns an iterator over m in key order, for stable output.
func sortedMap(m map[string]string) func(func(string, string) bool) {
	return func(yield func(string, string) bool) {
		for _, k := range slices.Sorted(maps.Keys(m)) {
			if !yield(k, m[k]) {
				return
			}
		}
	}
}

func neighborOf(text string) string {
	f := strings.Fields(strings.TrimPrefix(text, "no "))
	if len(f) >= 2 && f[0] == "neighbor" {
		return f[1]
	}
	return ""
}

func exitFor(header string) string {
	switch {
	case strings.HasPrefix(header, "address-family"):
		return "exit-address-family"
	case strings.HasPrefix(header, "vrf "):
		return "exit-vrf"
	}
	return "exit"
}
