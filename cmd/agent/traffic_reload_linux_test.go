// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nezhahq/agent/model"
	"github.com/nezhahq/agent/pkg/traffic"
	"sigs.k8s.io/yaml"
)

func TestLocalTrafficReloadPublishesOnlyValidLocalPolicy(t *testing.T) {
	original := runtimeConfigSnapshot.Load()
	t.Cleanup(func() {
		runtimeConfigSnapshot.Store(original)
	})
	path := filepath.Join(t.TempDir(), "config.yml")
	cfg := model.AgentConfig{Server: "example.invalid:5555", ClientSecret: "example-placeholder", UUID: "00000000-0000-0000-0000-000000000001", Traffic: &traffic.Config{Include: []string{"public"}, Report: "billing", StateFile: filepath.Join(t.TempDir(), "state.json")}}
	publishRuntimeConfig(cfg)
	local := cfg.Clone()
	local.Traffic.Weights = map[string]traffic.Weights{"public": {TXToOut: 2 * traffic.Scale}}
	local.Server = "other.invalid:5555"
	write := func() {
		t.Helper()
		data, err := yaml.Marshal(local)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	stop := startTrafficReload(path)
	t.Cleanup(stop)
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for loadRuntimeConfig().Traffic.Weights["public"].TXToOut != 2*traffic.Scale && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !reflect.DeepEqual(loadRuntimeConfig().Traffic, local.Traffic) {
		t.Fatal("SIGHUP did not publish the local policy")
	}
	if loadRuntimeConfig().Server != cfg.Server {
		t.Fatal("SIGHUP applied an unrelated configuration change")
	}
	local.Traffic.Report = "raw"
	write()
	if err := reloadLocalTraffic(path); err == nil {
		t.Fatal("hot reload changed reporting semantics")
	}
	if loadRuntimeConfig().Traffic.Report != "billing" {
		t.Fatal("rejected reload was published")
	}
	local.Traffic = nil
	write()
	if err := reloadLocalTraffic(path); err == nil {
		t.Fatal("hot reload disabled traffic")
	}
	local.Traffic = cfg.Traffic.Clone()
	local.Traffic.StateFile += ".different"
	write()
	if err := reloadLocalTraffic(path); err == nil {
		t.Fatal("hot reload changed the ledger path")
	}
	local.Traffic.StateFile = cfg.Traffic.StateFile
	local.Traffic.Include = []string{"invalid*"}
	write()
	if err := reloadLocalTraffic(path); err == nil {
		t.Fatal("hot reload accepted an invalid selector")
	}
}

func TestPreRunTrafficFailsBeforePublishingCorruptLedger(t *testing.T) {
	original := runtimeConfigSnapshot.Load()
	working := agentConfig.Clone()
	t.Cleanup(func() {
		runtimeConfigSnapshot.Store(original)
		agentConfig = working
	})
	start := publishRuntimeConfig(model.AgentConfig{Server: "sentinel"})
	dir := t.TempDir()
	path, ledger := filepath.Join(dir, "config.yml"), filepath.Join(dir, "ledger.json")
	config := "server: example.invalid:5555\nclient_secret: placeholder\nuuid: 00000000-0000-0000-0000-000000000001\n" +
		"traffic:\n  include: [synthetic0]\n  report: billing\n  state_file: " + ledger + "\n"
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger, []byte(`{"schema":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := preRun(path); err == nil || !strings.Contains(err.Error(), "traffic checkpoint unreadable") {
		t.Fatalf("startup did not reject corrupt ledger: %v", err)
	}
	if loadRuntimeConfig() != start {
		t.Fatal("failed startup published a new runtime configuration")
	}
}
