// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

// Package traffic implements the local, opt-in traffic ledger for this fork.
package traffic

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

const Scale uint64 = 1_000_000

// Weights maps observed RX/TX into estimated inbound/outbound bytes, in ppm.
// One million means 1x. An explicit all-zero rule contributes no billed bytes.
type Weights struct {
	RXToIn  uint64 `json:"rx_to_in_ppm" koanf:"rx_to_in_ppm"`
	TXToIn  uint64 `json:"tx_to_in_ppm" koanf:"tx_to_in_ppm"`
	RXToOut uint64 `json:"rx_to_out_ppm" koanf:"rx_to_out_ppm"`
	TXToOut uint64 `json:"tx_to_out_ppm" koanf:"tx_to_out_ppm"`
}

type Config struct {
	Include   []string           `json:"include" koanf:"include"`
	Exclude   []string           `json:"exclude,omitempty" koanf:"exclude"`
	Weights   map[string]Weights `json:"weights,omitempty" koanf:"weights"`
	Report    string             `json:"report" koanf:"report"`
	StateFile string             `json:"state_file" koanf:"state_file"`
}

func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	out := *c
	out.Include = slices.Clone(c.Include)
	out.Exclude = slices.Clone(c.Exclude)
	out.Weights = maps.Clone(c.Weights)
	return &out
}

func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	if len(c.Include) == 0 {
		return errors.New("traffic.include must explicitly name at least one interface")
	}
	if c.Report != "raw" && c.Report != "billing" {
		return errors.New("traffic.report must be raw or billing")
	}
	if !filepath.IsAbs(c.StateFile) || filepath.Clean(c.StateFile) != c.StateFile || filepath.Dir(c.StateFile) == c.StateFile {
		return errors.New("traffic.state_file must be a clean absolute path")
	}
	for _, names := range [][]string{c.Include, c.Exclude} {
		seen := map[string]bool{}
		for _, name := range names {
			if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/*?[]\x00\n\r") || seen[name] {
				return errors.New("traffic interface names must be unique exact names, without globs")
			}
			seen[name] = true
		}
	}
	for name, w := range c.Weights {
		if !slices.Contains(c.Include, name) {
			return fmt.Errorf("traffic weight for %q is outside include", name)
		}
		for _, v := range []uint64{w.RXToIn, w.TXToIn, w.RXToOut, w.TXToOut} {
			if v > 1000*Scale {
				return errors.New("traffic weights must be between 0 and 1000000000 ppm (1000x)")
			}
		}
	}
	return nil
}

func (c *Config) Select(name string) (bool, string, Weights) {
	if slices.Contains(c.Exclude, name) {
		return false, "excluded", Weights{}
	}
	if !slices.Contains(c.Include, name) {
		return false, "not_included", Weights{}
	}
	w, ok := c.Weights[name]
	if !ok {
		w = Weights{RXToIn: Scale, TXToOut: Scale}
	}
	return true, "included", w
}
