package kernel

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/netip"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
)

// DropRule drops IPv6 packets in the prerouting hook, i.e. packets as they
// arrive, before any routing or SRv6 processing. All set fields must match.
type DropRule struct {
	Name      string       // shown in status, stored as the rule's user data
	IifPrefix string       // incoming interface name prefix ("dcibr"), "" = any
	ExceptIif string       // exact incoming interface name that never matches ("lo")
	Daddr     netip.Prefix // destination inside, zero = any
	Saddr     netip.Prefix // source inside, zero = any
	SaddrNot  netip.Prefix // source outside, zero = any
	// ExceptSaddr never matches: sources inside it are exempt
	ExceptSaddr netip.Prefix
}

func (r DropRule) String() string {
	var f []string
	if r.IifPrefix != "" {
		f = append(f, fmt.Sprintf("iifname %q", r.IifPrefix+"*"))
	}
	if r.ExceptIif != "" {
		f = append(f, fmt.Sprintf("iifname != %q", r.ExceptIif))
	}
	if r.Saddr.IsValid() {
		f = append(f, "ip6 saddr "+r.Saddr.String())
	}
	if r.SaddrNot.IsValid() {
		f = append(f, "ip6 saddr != "+r.SaddrNot.String())
	}
	if r.ExceptSaddr.IsValid() {
		f = append(f, "ip6 saddr != "+r.ExceptSaddr.String())
	}
	if r.Daddr.IsValid() {
		f = append(f, "ip6 daddr "+r.Daddr.String())
	}
	return strings.Join(append(f, "drop"), " ")
}

// FilterCounter is a rule's name and the packets it dropped.
type FilterCounter struct {
	Name    string
	Packets uint64
}

// EnsureFilter makes the ip6 table exist with exactly the given drop rules in
// a prerouting chain. The rules carry a digest of the whole set, so an
// unchanged filter is left alone (and keeps its counters); a changed one is
// replaced in one atomic transaction. No rules deletes the table.
func EnsureFilter(table string, rules []DropRule) error {
	c, err := nftables.New()
	if err != nil {
		return err
	}
	digest := filterDigest(rules)
	t := &nftables.Table{Name: table, Family: nftables.TableFamilyIPv6}
	if cur, err := currentDigest(c, t); err == nil && cur == digest && len(rules) > 0 {
		return nil
	}
	if tables, err := c.ListTablesOfFamily(nftables.TableFamilyIPv6); err == nil {
		for _, x := range tables {
			if x.Name == table {
				c.DelTable(t)
			}
		}
	}
	if len(rules) > 0 {
		c.AddTable(t)
		policy := nftables.ChainPolicyAccept
		ch := c.AddChain(&nftables.Chain{
			Name: "prerouting", Table: t, Type: nftables.ChainTypeFilter,
			Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityRaw, Policy: &policy,
		})
		for _, r := range rules {
			c.AddRule(&nftables.Rule{Table: t, Chain: ch, Exprs: r.exprs(), UserData: []byte(digest + " " + r.Name)})
		}
	}
	if err := c.Flush(); err != nil {
		return fmt.Errorf("nftables %s: %w", table, err)
	}
	return nil
}

// FilterCounters returns the drop counters of the table's rules.
func FilterCounters(table string) ([]FilterCounter, error) {
	c, err := nftables.New()
	if err != nil {
		return nil, err
	}
	t := &nftables.Table{Name: table, Family: nftables.TableFamilyIPv6}
	rules, err := c.GetRules(t, &nftables.Chain{Name: "prerouting", Table: t})
	if err != nil {
		return nil, err
	}
	var out []FilterCounter
	for _, r := range rules {
		fc := FilterCounter{Name: string(r.UserData)}
		if _, name, ok := strings.Cut(fc.Name, " "); ok {
			fc.Name = name
		}
		for _, e := range r.Exprs {
			if cnt, ok := e.(*expr.Counter); ok {
				fc.Packets = cnt.Packets
			}
		}
		out = append(out, fc)
	}
	return out, nil
}

func filterDigest(rules []DropRule) string {
	h := sha256.New()
	for _, r := range rules {
		fmt.Fprintf(h, "%s|%s\n", r.Name, r)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func currentDigest(c *nftables.Conn, t *nftables.Table) (string, error) {
	rules, err := c.GetRules(t, &nftables.Chain{Name: "prerouting", Table: t})
	if err != nil || len(rules) == 0 {
		return "", fmt.Errorf("no rules")
	}
	d, _, _ := strings.Cut(string(rules[0].UserData), " ")
	return d, nil
}

// exprs builds: [meta iifname prefix] [ip6 saddr/daddr prefix matches] counter drop
func (r DropRule) exprs() []expr.Any {
	var e []expr.Any
	if r.IifPrefix != "" {
		e = append(e,
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			// comparing only the prefix's bytes matches "prefix*"
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(r.IifPrefix)},
		)
	}
	if r.ExceptIif != "" {
		name := make([]byte, 16) // IFNAMSIZ, NUL-padded: an exact match
		copy(name, r.ExceptIif)
		e = append(e,
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: name},
		)
	}
	match := func(offset uint32, p netip.Prefix, op expr.CmpOp) []expr.Any {
		addr := p.Addr().As16()
		mask := maskBytes(p.Bits())
		return []expr.Any{
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 16},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 16, Mask: mask, Xor: make([]byte, 16)},
			&expr.Cmp{Op: op, Register: 1, Data: and(addr[:], mask)},
		}
	}
	const saddr, daddr = 8, 24 // offsets in the IPv6 header
	if r.Saddr.IsValid() {
		e = append(e, match(saddr, r.Saddr, expr.CmpOpEq)...)
	}
	if r.SaddrNot.IsValid() {
		e = append(e, match(saddr, r.SaddrNot, expr.CmpOpNeq)...)
	}
	if r.ExceptSaddr.IsValid() {
		e = append(e, match(saddr, r.ExceptSaddr, expr.CmpOpNeq)...)
	}
	if r.Daddr.IsValid() {
		e = append(e, match(daddr, r.Daddr, expr.CmpOpEq)...)
	}
	return append(e, &expr.Counter{}, &expr.Verdict{Kind: expr.VerdictDrop})
}

func maskBytes(bits int) []byte {
	m := make([]byte, 16)
	for i := 0; i < bits; i++ {
		m[i/8] |= 0x80 >> (i % 8)
	}
	return m
}

func and(a, b []byte) []byte {
	out := bytes.Clone(a)
	for i := range out {
		out[i] &= b[i]
	}
	return out
}
