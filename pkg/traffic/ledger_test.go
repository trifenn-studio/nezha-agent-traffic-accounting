// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package traffic

import (
	"math"
	"math/big"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{Include: []string{"public", "relay"}, Report: "billing", StateFile: filepath.Join(t.TempDir(), "ledger.json"), Weights: map[string]Weights{
		"relay": {RXToIn: Scale, RXToOut: Scale, TXToOut: 2 * Scale},
	}}
}

func advance(t *testing.T, s State, c *Config, boot string, cs ...Counter) State {
	t.Helper()
	next, err := Advance(s, c, boot, s.At.Add(time.Second), cs)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestPathEstimateAndPolicyBoundary(t *testing.T) {
	c := testConfig(t)
	p := Counter{Name: "public", ID: "1/a", RX: 1000, TX: 2000}
	r := Counter{Name: "relay", ID: "2/b", RX: 500, TX: 700}
	s := advance(t, State{}, c, "boot", p, r)
	if s.Billed != (Pair{}) {
		t.Fatal("first observation must not price historical counters")
	}
	p.RX += 100
	p.TX += 200
	r.RX += 30
	r.TX += 40
	s = advance(t, s, c, "boot", p, r)
	if s.Raw != (Pair{130, 240}) || s.Billed != (Pair{130, 310}) {
		t.Fatalf("path estimate: %+v %+v", s.Raw, s.Billed)
	}
	c.Weights["relay"] = Weights{RXToIn: Scale, RXToOut: 2 * Scale, TXToOut: 3 * Scale}
	p.RX += 20
	p.TX += 30
	r.RX += 20
	r.TX += 10
	s = advance(t, s, c, "boot", p, r)
	if s.Billed.Out != 380 {
		t.Fatalf("reload repriced the old interval: %d", s.Billed.Out)
	}
	p.RX += 10
	p.TX += 20
	r.RX += 10
	r.TX += 10
	s = advance(t, s, c, "boot", p, r)
	if s.Billed.Out != 450 {
		t.Fatalf("new policy did not apply: %d", s.Billed.Out)
	}
}

func TestTopologyLifecycleDoesNotRepriceHistory(t *testing.T) {
	c := testConfig(t)
	p := Counter{Name: "public", ID: "1/a", RX: 100, TX: 200}
	s := advance(t, State{}, c, "A", p)
	p.RX = 120
	p.TX = 230
	s = advance(t, s, c, "A", p)
	// A reset counts only bytes on the new counter, independently per direction.
	p.RX = 3
	p.TX = 240
	s = advance(t, s, c, "A", p)
	if s.Raw != (Pair{23, 40}) {
		t.Fatal(s.Raw)
	}
	// Rename keeps identity: the old policy owns this final interval.
	p.Name = "renamed"
	p.RX = 8
	p.TX = 245
	s = advance(t, s, c, "A", p)
	if s.Raw != (Pair{28, 45}) {
		t.Fatal(s.Raw)
	}
	p.RX = 15
	p.TX = 250
	s = advance(t, s, c, "A", p)
	if s.Raw != (Pair{28, 45}) {
		t.Fatal("unselected renamed interface contributed")
	}
	c.Include = []string{"renamed"}
	c.Weights = nil
	s = advance(t, s, c, "A", p)
	p.RX = 20
	p.TX = 260
	s = advance(t, s, c, "A", p)
	if s.Raw != (Pair{33, 55}) {
		t.Fatal(s.Raw)
	}
	s = advance(t, s, c, "A") // Disappearance drops identity baselines, never totals.
	p.RX = 900000
	p.TX = 800000
	s = advance(t, s, c, "A", p)
	if s.Raw != (Pair{33, 55}) {
		t.Fatal("reappearance imported unseen history")
	}
	p.ID = "99/new"
	p.RX = 1000000
	s = advance(t, s, c, "A", p)
	if s.Raw != (Pair{33, 55}) {
		t.Fatal("recreated interface imported unseen history")
	}
	p.RX = 1000001
	p.TX = 800001
	s = advance(t, s, c, "A", p)
	if s.Raw != (Pair{34, 56}) {
		t.Fatal(s.Raw)
	}
	p.RX = 100
	p.TX = 200
	s = advance(t, s, c, "B", p)
	if s.Raw != (Pair{34, 56}) {
		t.Fatal("boot change must retain totals and baseline counters")
	}
	p.RX = 105
	p.TX = 207
	s = advance(t, s, c, "B", p)
	if s.Raw != (Pair{39, 63}) {
		t.Fatal(s.Raw)
	}
}

func TestStrictSelectionAndExplainedOverlap(t *testing.T) {
	c := testConfig(t)
	c.Include = []string{"public", "synthetic-excluded", "synthetic-tunnel", "missing"}
	c.Exclude = []string{"synthetic-excluded"}
	c.Weights = nil
	s := advance(t, State{}, c, "boot",
		Counter{Name: "public", ID: "1"}, Counter{Name: "synthetic-excluded", ID: "2", Kind: "tunnel"},
		Counter{Name: "synthetic-tunnel", ID: "3", Kind: "tunnel", Lower: []string{"public"}}, Counter{Name: "other", ID: "4"})
	selected := map[string]bool{}
	for _, o := range s.Audit.Interfaces {
		selected[o.Counter.Name] = o.Selected
	}
	if !reflect.DeepEqual(selected, map[string]bool{"public": true, "synthetic-excluded": false, "synthetic-tunnel": true, "other": false}) {
		t.Fatal(selected)
	}
	warnings := strings.Join(s.Audit.Warnings, " ")
	for _, term := range []string{"missing", "tunnel synthetic-tunnel", "carrier public"} {
		if !strings.Contains(warnings, term) {
			t.Fatal(warnings)
		}
	}
}

func TestFractionalAccountingIsIndependentOfSamplingPartitions(t *testing.T) {
	random := rand.New(rand.NewSource(47))
	for range 100 {
		c := testConfig(t)
		w := Weights{RXToIn: uint64(random.Intn(3000000)), TXToIn: uint64(random.Intn(3000000)), RXToOut: uint64(random.Intn(3000000)), TXToOut: uint64(random.Intn(3000000))}
		c.Weights["public"] = w
		p := Counter{Name: "public", ID: "1"}
		baseline := advance(t, State{}, c, "boot", p)
		partitioned := baseline
		for range 50 {
			p.RX += uint64(random.Intn(1000))
			p.TX += uint64(random.Intn(1000))
			partitioned = advance(t, partitioned, c, "boot", p)
		}
		single := advance(t, baseline, c, "boot", p)
		if single.Billed != partitioned.Billed || single.Remainder != partitioned.Remainder {
			t.Fatal("sample partition changed the bill estimate")
		}
		// Independent arbitrary-precision dot product checks integer arithmetic.
		expected := func(a, b uint64) (uint64, uint64) {
			n := new(big.Int).Add(new(big.Int).Mul(new(big.Int).SetUint64(p.RX), new(big.Int).SetUint64(a)), new(big.Int).Mul(new(big.Int).SetUint64(p.TX), new(big.Int).SetUint64(b)))
			q, r := new(big.Int), new(big.Int)
			q.QuoRem(n, new(big.Int).SetUint64(Scale), r)
			return q.Uint64(), r.Uint64()
		}
		in, ri := expected(w.RXToIn, w.TXToIn)
		out, ro := expected(w.RXToOut, w.TXToOut)
		if single.Billed != (Pair{in, out}) || single.Remainder != (Pair{ri, ro}) {
			t.Fatal("weighted arithmetic differs from exact dot product")
		}
	}
}

func TestOverflowAndInvalidSamplesAreTransactional(t *testing.T) {
	c := testConfig(t)
	p := Counter{Name: "public", ID: "1"}
	s := advance(t, State{}, c, "boot", p)
	s.Raw.Out = math.MaxUint64 - 1
	before := cloneState(s)
	p.TX = 2
	if _, err := Advance(s, c, "boot", time.Now(), []Counter{p}); err == nil {
		t.Fatal("expected total overflow")
	}
	if !reflect.DeepEqual(s, before) {
		t.Fatal("failed sample mutated checkpoint")
	}
	c.Weights["public"] = Weights{TXToOut: 1000 * Scale}
	s = advance(t, State{}, c, "boot", Counter{Name: "public", ID: "1"})
	p.TX = math.MaxUint64
	if _, err := Advance(s, c, "boot", time.Now(), []Counter{p}); err == nil {
		t.Fatal("expected multiplication overflow")
	}
	if _, err := Advance(s, c, "boot", time.Now(), []Counter{p, p}); err == nil {
		t.Fatal("accepted duplicate identity")
	}
	c.Report = "raw"
	if _, err := Advance(s, c, "boot", time.Now(), []Counter{p}); err == nil {
		t.Fatal("changed semantics on the same ledger")
	}
}
