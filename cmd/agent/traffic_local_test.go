// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nezhahq/agent/model"
	"github.com/nezhahq/agent/pkg/traffic"
	pb "github.com/nezhahq/agent/proto"
)

func TestRemoteApplyPreservesLocalTrafficPolicy(t *testing.T) {
	policy := &traffic.Config{Include: []string{"public"}, Report: "raw", StateFile: filepath.Join(t.TempDir(), "state.json")}
	cfg := model.AgentConfig{Traffic: policy}
	for _, body := range []string{`{"debug":true}`, `{"debug":true,"traffic":null}`, `{"traffic":{"report":"billing","state_file":"/unwanted"}}`, `{"TRAFFIC":false}`} {
		result := new(pb.TaskResult)
		updated, ok := parseApplyConfigLocked(&cfg, &pb.Task{Data: body}, result)
		if !ok {
			t.Fatal(result.Data)
		}
		if !reflect.DeepEqual(updated.Traffic, policy) {
			t.Fatal("remote edit changed local traffic policy")
		}
		updated.Traffic.Include[0] = "mutated"
		if policy.Include[0] != "public" {
			t.Fatal("remote edit retained policy alias")
		}
	}
	result := new(pb.TaskResult)
	if _, ok := parseApplyConfigLocked(&cfg, &pb.Task{Data: `{"nic_allowlist":{"synthetic0":true}}`}, result); ok {
		t.Fatal("remote edit installed a conflicting selector")
	}
	handleReportConfigTaskWithConfig(&cfg, result)
	var reported map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.Data), &reported); err != nil || !result.Successful || reported["traffic"] != nil {
		t.Fatalf("Dashboard config contains local traffic policy: %s, %v", result.Data, err)
	}
}

func TestDiagnosticsReadOnlyAndUpdateGuard(t *testing.T) {
	dir := t.TempDir()
	path, statePath := filepath.Join(dir, "config.yml"), filepath.Join(dir, "state.json")
	body := "traffic:\n  include: [public]\n  report: raw\n  state_file: " + statePath + "\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	policy, err := model.ReadTrafficConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := traffic.Advance(traffic.State{}, policy, "synthetic-boot", time.Now(), []traffic.Counter{{Name: "public", ID: "1", RX: 10, TX: 20}})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, checkpoint, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"nezha-agent", "-c", path, "traffic-status"},
		{"nezha-agent", "traffic-status", "--config", path},
	} {
		app := newAgentApp()
		var output bytes.Buffer
		app.Writer = &output
		if err := app.Run(args); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bytes.TrimSpace(output.Bytes()), checkpoint) {
			t.Fatal("unexpected checkpoint JSON", output.String())
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != body {
		t.Fatal("diagnostics modified configuration")
	}
	if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, checkpoint) {
		t.Fatal("diagnostics modified the checkpoint", err)
	}
	if _, err := os.Stat(statePath + ".lock"); !os.IsNotExist(err) {
		t.Fatal("diagnostics created a lock file", err)
	}
	if _, err := model.ReadTrafficConfig(path + ".missing"); err == nil {
		t.Fatal("missing config accepted")
	}
	if _, err := os.Stat(path + ".missing"); !os.IsNotExist(err) {
		t.Fatal("diagnostics created a file")
	}
	config := updateConfigTupleFrom(&model.AgentConfig{Traffic: policy})
	if !config.trafficEnabled || updateConfigTupleFrom(&model.AgentConfig{}).trafficEnabled {
		t.Fatal("update guard did not follow local traffic mode")
	}
	if doSelfUpdate(config, false) || doSelfUpdate(config, true) {
		t.Fatal("traffic mode allowed official self-update")
	}
}
