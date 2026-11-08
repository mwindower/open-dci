package kernel

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// DropRule drops packets in the prerouting hook, i.e. packets as they
// arrive, before any routing, SRv6 or VXLAN processing. All set fields must
// match. The addresses decide the family: IPv6 (table "ip6"), or IPv4 (table
// "ip"); a rule doesn't mix them.
type DropRule struct {
	Name      string       // shown in status, stored as the rule's user data
	IifPrefix string       // incoming interface name prefix ("dcibr"), "" = any
	ExceptIif []string     // exact incoming interface names that never match ("lo")
	Daddr     netip.Prefix // destination inside, zero = any
	Saddr     netip.Prefix // source inside, zero = any
	// SaddrNot: the source is outside all of them (sources inside one are
	// exempt); empty = any
	SaddrNot []netip.Prefix
	UDPDport uint16 // UDP destination port, 0 = any protocol
}

// IPv4 reports whether the rule matches IPv4 packets.
func (r DropRule) IPv4() bool {
	for _, p := range append([]netip.Prefix{r.Daddr, r.Saddr}, r.SaddrNot...) {
		if p.IsValid() {
			return p.Addr().Is4()
		}
	}
	return false
}

func (r DropRule) String() string {
	var f []string
	if r.IifPrefix != "" {
		f = append(f, fmt.Sprintf("iifname %q", r.IifPrefix+"*"))
	}
	for _, n := range r.ExceptIif {
		f = append(f, fmt.Sprintf("iifname != %q", n))
	}
	ip := "ip6"
	if r.IPv4() {
		ip = "ip"
	}
	if r.Saddr.IsValid() {
		f = append(f, ip+" saddr "+r.Saddr.String())
	}
	for _, p := range r.SaddrNot {
		f = append(f, ip+" saddr != "+p.String())
	}
	if r.Daddr.IsValid() {
		f = append(f, ip+" daddr "+r.Daddr.String())
	}
	if r.UDPDport != 0 {
		f = append(f, fmt.Sprintf("udp dport %d", r.UDPDport))
	}
	return strings.Join(append(f, "drop"), " ")
}

// FilterCounter is a rule's name and the packets it dropped.
type FilterCounter struct {
	Name    string
	Packets uint64
}

// EnsureFilter makes the tables (ip6 and ip, both named table) exist with
// exactly the given drop rules in a prerouting chain. The rules carry a
// digest of the table's whole set, so an unchanged filter is left alone (and
// keeps its counters); a changed one is replaced in one atomic transaction.
// No rules of a family deletes its table.
func EnsureFilter(table string, rules []DropRule) error {
	var v4, v6 []DropRule
	for _, r := range rules {
		if r.IPv4() {
			v4 = append(v4, r)
		} else {
			v6 = append(v6, r)
		}
	}
	if err := ensureFilter(table, nftables.TableFamilyIPv6, v6); err != nil {
		return err
	}
	return ensureFilter(table, nftables.TableFamilyIPv4, v4)
}

func ensureFilter(table string, family nftables.TableFamily, rules []DropRule) error {
	c, err := nftables.New()
	if err != nil {
		return err
	}
	digest := filterDigest(rules)
	t := &nftables.Table{Name: table, Family: family}
	if cur, err := currentDigest(c, t); err == nil && cur == digest && len(rules) > 0 {
		return nil
	}
	if tables, err := c.ListTablesOfFamily(family); err == nil {
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

// FilterCounters returns the drop counters of the tables' rules (ip6, then
// ip; a missing ip table has no rules).
func FilterCounters(table string) ([]FilterCounter, error) {
	c, err := nftables.New()
	if err != nil {
		return nil, err
	}
	var out []FilterCounter
	for _, family := range []nftables.TableFamily{nftables.TableFamilyIPv6, nftables.TableFamilyIPv4} {
		t := &nftables.Table{Name: table, Family: family}
		rules, err := c.GetRules(t, &nftables.Chain{Name: "prerouting", Table: t})
		if err != nil {
			if family == nftables.TableFamilyIPv4 {
				break
			}
			return nil, err
		}
		out = append(out, counters(rules)...)
	}
	return out, nil
}

func counters(rules []*nftables.Rule) []FilterCounter {
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
	return out
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

// exprs builds: [meta iifname prefix] [saddr/daddr prefix matches] [udp dport] counter drop
func (r DropRule) exprs() []expr.Any {
	var e []expr.Any
	if r.IifPrefix != "" {
		e = append(e,
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			// comparing only the prefix's bytes matches "prefix*"
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(r.IifPrefix)},
		)
	}
	for _, n := range r.ExceptIif {
		name := make([]byte, 16) // IFNAMSIZ, NUL-padded: an exact match
		copy(name, n)
		e = append(e,
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: name},
		)
	}
	match := func(offset uint32, p netip.Prefix, op expr.CmpOp) []expr.Any {
		addr := p.Addr().AsSlice()
		n := uint32(len(addr))
		mask := maskBytes(p.Bits())[:n]
		return []expr.Any{
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: n},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: n, Mask: mask, Xor: make([]byte, n)},
			&expr.Cmp{Op: op, Register: 1, Data: and(addr, mask)},
		}
	}
	var saddr, daddr uint32 = 8, 24 // offsets in the IPv6 header
	if r.IPv4() {
		saddr, daddr = 12, 16
	}
	if r.Saddr.IsValid() {
		e = append(e, match(saddr, r.Saddr, expr.CmpOpEq)...)
	}
	for _, p := range r.SaddrNot {
		e = append(e, match(saddr, p, expr.CmpOpNeq)...)
	}
	if r.Daddr.IsValid() {
		e = append(e, match(daddr, r.Daddr, expr.CmpOpEq)...)
	}
	if r.UDPDport != 0 {
		e = append(e,
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_UDP}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binary.BigEndian.AppendUint16(nil, r.UDPDport)},
		)
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
