//go:build linux || darwin || freebsd

// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nezhahq/agent/model"
	"github.com/nezhahq/agent/pkg/traffic"
)

func TestCustomTrafficRecoveryAndWireMetrics(t *testing.T) {
	oldNow, oldProbe := networkNow, trafficProbe
	oldIn, oldOut, oldIS, oldOS, oldAt := netInTransfer, netOutTransfer, netInSpeed, netOutSpeed, lastUpdateNetStats
	t.Cleanup(func() {
		if trafficRuntime.meter != nil {
			trafficRuntime.meter.Close()
		}
		trafficRuntime.meter = nil
		trafficRuntime.lastAt = time.Time{}
		trafficRuntime.lastRaw = traffic.Pair{}
		trafficRuntime.lastError = ""
		networkNow, trafficProbe = oldNow, oldProbe
		netInTransfer, netOutTransfer, netInSpeed, netOutSpeed, lastUpdateNetStats = oldIn, oldOut, oldIS, oldOS, oldAt
	})
	policy := &traffic.Config{Include: []string{"relay"}, Report: "billing", StateFile: filepath.Join(t.TempDir(), "ledger.json"), Weights: map[string]traffic.Weights{"relay": {RXToIn: traffic.Scale, RXToOut: traffic.Scale, TXToOut: 2 * traffic.Scale}}}
	c := traffic.Counter{Name: "relay", ID: "1", RX: 10000, TX: 20000}
	m, err := traffic.Open(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Sample(policy, "boot", time.Unix(100, 0), []traffic.Counter{c}); err != nil {
		t.Fatal(err)
	}
	c.RX += 10
	c.TX += 20
	if _, err := m.Sample(policy, "boot", time.Unix(101, 0), []traffic.Counter{c}); err != nil {
		t.Fatal(err)
	}
	m.Close()
	if err := InitTraffic(policy); err != nil {
		t.Fatal(err)
	}
	if netInTransfer != 10 || netOutTransfer != 50 {
		t.Fatal("startup did not restore durable report before any failed probe")
	}
	at := time.Unix(105, 0)
	networkNow = func() time.Time { return at }
	trafficProbe = func(context.Context) (string, []traffic.Counter, error) { return "boot", []traffic.Counter{c}, nil }
	cfg := &model.AgentConfig{Traffic: policy}
	TrackNetworkSpeed(cfg)
	if netInSpeed != 0 || netOutSpeed != 0 {
		t.Fatal("startup total produced an artificial speed spike")
	}
	at = at.Add(2 * time.Second)
	c.RX += 8
	c.TX += 12
	TrackNetworkSpeed(cfg)
	host := model.HostState{NetInTransfer: netInTransfer, NetOutTransfer: netOutTransfer, NetInSpeed: netInSpeed, NetOutSpeed: netOutSpeed}
	wire := host.PB()
	if wire.NetInTransfer != 18 || wire.NetOutTransfer != 82 || wire.NetInSpeed != 4 || wire.NetOutSpeed != 6 {
		t.Fatal("wire must carry billing totals and raw rates", wire)
	}
	trafficProbe = func(context.Context) (string, []traffic.Counter, error) {
		return "", nil, errors.New("probe unavailable")
	}
	TrackNetworkSpeed(cfg)
	if netInTransfer != 18 || netOutTransfer != 82 || netInSpeed != 0 || netOutSpeed != 0 {
		t.Fatal("probe failure must hold cumulative report and zero speeds")
	}
}
