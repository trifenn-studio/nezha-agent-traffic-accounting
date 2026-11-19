// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nezhahq/agent/pkg/traffic"
)

func TestTrafficConfigRoundTripAndClone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	body := "server: example.invalid:5555\nclient_secret: example-placeholder\nuuid: 00000000-0000-0000-0000-000000000001\ntraffic:\n  include: [public, relay]\n  exclude: [synthetic-excluded]\n  report: billing\n  state_file: " + filepath.Join(t.TempDir(), "state.json") + "\n  weights:\n    relay:\n      rx_to_in_ppm: 1000000\n      rx_to_out_ppm: 1000000\n      tx_to_out_ppm: 2000000\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var cfg AgentConfig
	t.Setenv("NZ_TRAFFIC", "not-a-local-policy")
	if err := cfg.Read(path); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Traffic.Weights["relay"]; got.RXToOut != traffic.Scale || got.TXToOut != 2*traffic.Scale {
		t.Fatal("nested koanf weights", got)
	}
	original := cfg.Traffic.Clone()
	cloned := cfg.Clone()
	cfg.Traffic.Include[0] = "mutated"
	cfg.Traffic.Exclude[0] = "mutated"
	cfg.Traffic.Weights["relay"] = traffic.Weights{}
	if !reflect.DeepEqual(cloned.Traffic, original) {
		t.Fatal("config snapshot retained mutable policy aliases")
	}
	if err := cloned.Save(); err != nil {
		t.Fatal(err)
	}
	var restored AgentConfig
	if err := restored.Read(path); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Traffic, original) {
		t.Fatal("saving upstream settings lost local policy")
	}
}

func TestTrafficInvalidPoliciesAreRejected(t *testing.T) {
	valid := &traffic.Config{Include: []string{"public"}, Report: "raw", StateFile: filepath.Join(t.TempDir(), "state.json")}
	for _, edit := range []func(*traffic.Config){
		func(c *traffic.Config) { c.Include = nil }, func(c *traffic.Config) { c.Include = []string{"eth*"} },
		func(c *traffic.Config) { c.Include = []string{"public", "public"} }, func(c *traffic.Config) { c.StateFile = "relative.json" },
		func(c *traffic.Config) { c.Report = "both" }, func(c *traffic.Config) { c.Weights = map[string]traffic.Weights{"typo": {TXToOut: traffic.Scale}} },
		func(c *traffic.Config) {
			c.Weights = map[string]traffic.Weights{"public": {TXToOut: 1001 * traffic.Scale}}
		},
	} {
		c := valid.Clone()
		edit(c)
		cfg := AgentConfig{Traffic: c}
		if err := ValidateConfig(&cfg, true); err == nil {
			t.Fatalf("accepted invalid policy %+v", c)
		}
	}
	cfg := AgentConfig{Traffic: valid, NICAllowlist: map[string]bool{"eth0": true}}
	if err := ValidateConfig(&cfg, true); err == nil {
		t.Fatal("accepted two competing selection policies")
	}
}

func TestTrafficTyposAndConfigCollisionAreRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	base := "server: example.invalid:5555\nclient_secret: example-placeholder\nuuid: 00000000-0000-0000-0000-000000000001\ntraffic:\n  include: [public]\n  report: billing\n  state_file: " + filepath.Join(t.TempDir(), "state.json") + "\n"
	if err := os.WriteFile(path, []byte(base+"  weights:\n    public:\n      tx_to_out_pmm: 2000000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var cfg AgentConfig
	if err := cfg.Read(path); err == nil {
		t.Fatal("weight typo silently changed estimate")
	}
	cfg = AgentConfig{Traffic: &traffic.Config{Include: []string{"public"}, Report: "raw", StateFile: path}, filePath: path}
	if err := ValidateConfig(&cfg, true); err == nil {
		t.Fatal("ledger could replace the agent config")
	}
	t.Chdir(filepath.Dir(path))
	cfg.filePath = "config.yml"
	if err := ValidateConfig(&cfg, true); err == nil {
		t.Fatal("relative config path bypassed collision protection")
	}
	cfg.filePath = "config.yml.lock"
	if err := ValidateConfig(&cfg, true); err == nil {
		t.Fatal("ledger lock could alias the agent config")
	}
	cfg.filePath = path
	alias := filepath.Join(filepath.Dir(path), "alias.yml")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	cfg.Traffic.StateFile = alias
	if err := ValidateConfig(&cfg, true); err == nil {
		t.Fatal("hard-linked ledger could alias the agent config")
	}
}
