//go:build linux || darwin || freebsd

// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package traffic

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestRestartPersistenceLockAndFailureRecovery(t *testing.T) {
	c := testConfig(t)
	m, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	p := Counter{Name: "public", ID: "1", RX: 500, TX: 1000}
	if _, err := m.Sample(c, "boot", time.Now(), []Counter{p}); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(c); err == nil {
		other.Close()
		t.Fatal("two writers acquired one ledger")
	}
	p.RX += 10
	p.TX += 20
	persisted := m.persist
	m.persist = func(State) (bool, error) { return false, errors.New("disk full") }
	if _, err := m.Sample(c, "boot", time.Now(), []Counter{p}); err == nil {
		t.Fatal("reported unpersisted bytes")
	}
	if m.Checkpoint().Raw != (Pair{}) {
		t.Fatal("failed write moved baseline")
	}
	m.persist = persisted
	p.RX += 5
	p.TX += 7
	s, err := m.Sample(c, "boot", time.Now(), []Counter{p})
	if err != nil {
		t.Fatal(err)
	}
	if s.Raw != (Pair{15, 27}) {
		t.Fatal("recovery lost or duplicated increments", s.Raw)
	}
	if info, err := os.Stat(c.StateFile); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("checkpoint permissions", info, err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// Downtime has the persisted policy, even if the local config changed.
	c.Weights["public"] = Weights{RXToIn: Scale, TXToOut: 3 * Scale}
	p.TX += 11
	s, err = m.Sample(c, "boot", time.Now(), []Counter{p})
	if err != nil {
		t.Fatal(err)
	}
	if s.Billed.Out != 38 {
		t.Fatal("restart repriced downtime", s.Billed)
	}
	p.TX += 10
	s, err = m.Sample(c, "boot", time.Now(), []Counter{p})
	if err != nil {
		t.Fatal(err)
	}
	if s.Billed.Out != 68 {
		t.Fatal("post-restart policy", s.Billed)
	}
	s.Seen["1"] = Entry{}
	if m.Checkpoint().Seen["1"].Counter.ID != "1" {
		t.Fatal("returned checkpoint aliases meter")
	}
}

func TestCommittedButUnsyncedWriteDoesNotDoubleCharge(t *testing.T) {
	c := testConfig(t)
	m, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	p := Counter{Name: "public", ID: "1"}
	if _, err := m.Sample(c, "boot", time.Now(), []Counter{p}); err != nil {
		t.Fatal(err)
	}
	original := m.persist
	m.persist = func(s State) (bool, error) {
		committed, err := original(s)
		if err != nil {
			return committed, err
		}
		return true, errors.New("directory sync failed")
	}
	p.TX = 25
	if _, err := m.Sample(c, "boot", time.Now(), []Counter{p}); err == nil {
		t.Fatal("durability failure was hidden")
	}
	m.persist = original
	p.TX = 30
	s, err := m.Sample(c, "boot", time.Now(), []Counter{p})
	if err != nil {
		t.Fatal(err)
	}
	if s.Raw.Out != 30 {
		t.Fatal("commit-boundary retry duplicated bytes", s.Raw)
	}
}

func TestRawCheckpointAndColdModeSwitch(t *testing.T) {
	cfg := testConfig(t)
	cfg.Report = "raw"
	meter, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { meter.Close() })
	counter := Counter{Name: "relay", ID: "1", RX: 100, TX: 200}
	if _, err := meter.Sample(cfg, "boot", time.Now(), []Counter{counter}); err != nil {
		t.Fatal(err)
	}
	counter.RX += 5
	counter.TX += 11
	state, err := meter.Sample(cfg, "boot", time.Now(), []Counter{counter})
	if err != nil || state.Reported() != (Pair{5, 11}) || state.Billed != (Pair{5, 27}) {
		t.Fatalf("raw checkpoint reported %v, estimated %v: %v", state.Reported(), state.Billed, err)
	}
	if err := meter.Close(); err != nil {
		t.Fatal(err)
	}
	billing := cfg.Clone()
	billing.Report = "billing"
	if other, err := Open(billing); err == nil {
		other.Close()
		t.Fatal("reopened an existing raw ledger in billing mode")
	}
	billing.StateFile = filepath.Join(filepath.Dir(cfg.StateFile), "billing.json")
	newMeter, err := Open(billing)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { newMeter.Close() })
	baseline, err := newMeter.Sample(billing, "boot", time.Now(), []Counter{counter})
	if err != nil || baseline.Reported() != (Pair{}) {
		t.Fatalf("new billing ledger imported prior bytes: %v, %v", baseline.Reported(), err)
	}
	old, err := ReadState(cfg.StateFile)
	if err != nil || old.Reported() != state.Reported() {
		t.Fatalf("cold switch changed old ledger: %v, %v", old.Reported(), err)
	}
}

func TestCorruptCheckpointFailsClosed(t *testing.T) {
	c := testConfig(t)
	for _, data := range []string{"{", `{"schema":1}`, `{"schema":99}`, `null`, `{}`} {
		if err := os.WriteFile(c.StateFile, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if m, err := Open(c); err == nil {
			m.Close()
			t.Fatalf("accepted corrupt state %s", data)
		}
		after, _ := os.ReadFile(c.StateFile)
		if string(after) != data {
			t.Fatal("rewrote corrupt evidence")
		}
	}
}

func TestConcurrentRepeatedSampleIsChargedOnce(t *testing.T) {
	c := testConfig(t)
	m, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	p := Counter{Name: "public", ID: "1"}
	if _, err := m.Sample(c, "boot", time.Now(), []Counter{p}); err != nil {
		t.Fatal(err)
	}
	p.RX = 25
	p.TX = 100
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if _, err := m.Sample(c, "boot", time.Now(), []Counter{p}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	stored, err := ReadState(c.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Raw != (Pair{25, 100}) || !reflect.DeepEqual(stored, m.Checkpoint()) {
		t.Fatal("concurrent sample lost consistency")
	}
}
