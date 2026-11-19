// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package traffic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"sort"
	"time"
)

type Pair struct {
	In  uint64 `json:"in"`
	Out uint64 `json:"out"`
}

type Counter struct {
	Name  string   `json:"name"`
	ID    string   `json:"id"`
	RX    uint64   `json:"rx"`
	TX    uint64   `json:"tx"`
	Kind  string   `json:"kind,omitempty"`
	Lower []string `json:"lower,omitempty"`
}

type Entry struct {
	Counter  Counter `json:"counter"`
	Selected bool    `json:"selected"`
	Weights  Weights `json:"weights"`
}

type Observation struct {
	Entry
	Reason          string  `json:"reason"`
	Delta           Pair    `json:"observed_delta"`
	AppliedSelected bool    `json:"applied_selected"`
	AppliedWeights  Weights `json:"applied_weights"`
	Event           string  `json:"event,omitempty"`
}

type Audit struct {
	ConfigHash  string        `json:"config_hash"`
	Interfaces  []Observation `json:"interfaces"`
	Warnings    []string      `json:"warnings,omitempty"`
	RawDelta    Pair          `json:"selected_raw_delta"`
	BilledDelta Pair          `json:"estimated_billing_delta"`
}

// State is a checkpoint and the latest audit snapshot, never a cloud bill.
type State struct {
	Schema    int              `json:"schema"`
	Report    string           `json:"report"`
	Boot      string           `json:"boot_id"`
	At        time.Time        `json:"at"`
	Seen      map[string]Entry `json:"seen"`
	Raw       Pair             `json:"selected_raw_total"`
	Billed    Pair             `json:"estimated_billing_total"`
	Remainder Pair             `json:"billing_fraction_ppm"`
	Audit     Audit            `json:"audit"`
}

func (s State) Reported() Pair {
	if s.Report == "billing" {
		return s.Billed
	}
	return s.Raw
}

// Advance is transactional. The old sample's policy owns the interval ending
// at this sample, so a reload never reprices already observed bytes.
func Advance(old State, cfg *Config, boot string, now time.Time, counters []Counter) (State, error) {
	if err := cfg.Validate(); err != nil {
		return old, err
	}
	if cfg == nil || boot == "" {
		return old, errors.New("traffic configuration and boot identity are required")
	}
	if old.Schema != 0 && old.Schema != 1 {
		return old, errors.New("unsupported traffic state schema")
	}
	if old.Schema != 0 && old.Report != cfg.Report {
		return old, errors.New("traffic reporting mode cannot change on an existing ledger; plan a Dashboard migration")
	}
	next := old
	next.Schema, next.Report, next.Boot, next.At = 1, cfg.Report, boot, now.UTC()
	next.Seen = make(map[string]Entry, len(counters))
	encoded, _ := json.Marshal(cfg)
	digest := sha256.Sum256(encoded)
	next.Audit = Audit{ConfigHash: hex.EncodeToString(digest[:])}
	names := map[string]bool{}
	selected := map[string]Counter{}
	sorted := append([]Counter(nil), counters...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, cur := range sorted {
		if cur.ID == "" || cur.Name == "" || names[cur.Name] {
			return old, errors.New("missing or duplicate traffic interface identity/name")
		}
		if _, exists := next.Seen[cur.ID]; exists {
			return old, errors.New("duplicate traffic interface identity")
		}
		names[cur.Name] = true
		include, reason, w := cfg.Select(cur.Name)
		cur.Lower = slices.Clone(cur.Lower)
		entry := Entry{Counter: cur, Selected: include, Weights: w}
		next.Seen[cur.ID] = entry
		obs := Observation{Entry: entry, Reason: reason, Event: "baseline"}
		prev, exists := old.Seen[cur.ID]
		if exists && old.Boot == boot {
			obs.Event = ""
			obs.Delta.In = delta(cur.RX, prev.Counter.RX)
			obs.Delta.Out = delta(cur.TX, prev.Counter.TX)
			obs.AppliedSelected, obs.AppliedWeights = prev.Selected, prev.Weights
			if cur.RX < prev.Counter.RX || cur.TX < prev.Counter.TX {
				obs.Event = "counter_reset"
			}
			if cur.Name != prev.Counter.Name {
				if obs.Event == "" {
					obs.Event = "renamed"
				} else {
					obs.Event += " renamed"
				}
			}
			if prev.Selected {
				if err := accumulate(&next, obs.Delta, prev.Weights); err != nil {
					return old, err
				}
			}
		}
		next.Audit.Interfaces = append(next.Audit.Interfaces, obs)
		if include {
			selected[cur.Name] = cur
		}
	}
	next.Audit.RawDelta = Pair{In: next.Raw.In - old.Raw.In, Out: next.Raw.Out - old.Raw.Out}
	next.Audit.BilledDelta = Pair{In: next.Billed.In - old.Billed.In, Out: next.Billed.Out - old.Billed.Out}
	for _, name := range cfg.Include {
		if !names[name] {
			next.Audit.Warnings = append(next.Audit.Warnings, fmt.Sprintf("included interface %s is absent", name))
		}
	}
	for _, cur := range sorted {
		if _, ok := selected[cur.Name]; !ok {
			continue
		}
		if cur.Kind == "tunnel" && len(selected) > 1 {
			next.Audit.Warnings = append(next.Audit.Warnings, fmt.Sprintf("selected tunnel %s and other interfaces may count inner and outer packets; verify its carrier", cur.Name))
		}
		for _, lower := range cur.Lower {
			if _, ok := selected[lower]; ok {
				next.Audit.Warnings = append(next.Audit.Warnings, fmt.Sprintf("selected %s and carrier %s can overlap", cur.Name, lower))
			}
		}
	}
	if old.Schema != 0 && old.Boot != boot {
		next.Audit.Warnings = append(next.Audit.Warnings, "boot changed: retained totals, baselined current counters")
	}
	return next, nil
}

func delta(cur, prev uint64) uint64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

func add(dst *uint64, value uint64) error {
	n, carry := bits.Add64(*dst, value, 0)
	if carry != 0 {
		return errors.New("traffic accumulator overflow; report held at last checkpoint")
	}
	*dst = n
	return nil
}

func weighted(dst, rem *uint64, value, weight uint64) error {
	hi, lo := bits.Mul64(value, weight)
	lo, carry := bits.Add64(lo, *rem, 0)
	hi, carry = bits.Add64(hi, 0, carry)
	if carry != 0 || hi >= Scale {
		return errors.New("traffic weighted increment overflow")
	}
	q, r := bits.Div64(hi, lo, Scale)
	if err := add(dst, q); err != nil {
		return err
	}
	*rem = r
	return nil
}

func accumulate(s *State, d Pair, w Weights) error {
	if err := add(&s.Raw.In, d.In); err != nil {
		return err
	}
	if err := add(&s.Raw.Out, d.Out); err != nil {
		return err
	}
	for _, v := range []struct {
		dst, rem      *uint64
		value, weight uint64
	}{
		{&s.Billed.In, &s.Remainder.In, d.In, w.RXToIn},
		{&s.Billed.In, &s.Remainder.In, d.Out, w.TXToIn},
		{&s.Billed.Out, &s.Remainder.Out, d.In, w.RXToOut},
		{&s.Billed.Out, &s.Remainder.Out, d.Out, w.TXToOut},
	} {
		if err := weighted(v.dst, v.rem, v.value, v.weight); err != nil {
			return err
		}
	}
	return nil
}
