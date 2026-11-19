// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package traffic

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

// Uses real kernel counters and local UDP only: no cloud, root, or Dashboard.
func TestLinuxLiveCountersSurviveAgentRestart(t *testing.T) {
	cfg := testConfig(t)
	cfg.Include = []string{"lo"}
	cfg.Weights = map[string]Weights{"lo": {RXToIn: Scale, RXToOut: Scale, TXToOut: 2 * Scale}}
	meter, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer meter.Close()
	boot, counters, err := Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := meter.Sample(cfg, boot, time.Now(), counters); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	sender, err := net.Dial("udp4", listener.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	payload := bytes.Repeat([]byte("x"), 4096)
	if _, err := sender.Write(payload); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8192)
	if n, _, err := listener.ReadFrom(buffer); err != nil || n != len(payload) {
		t.Fatal("local UDP", n, err)
	}
	// Recover from disk rather than reusing the in-memory baseline.
	meter.Close()
	meter, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer meter.Close()
	boot, counters, err = Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state, err := meter.Sample(cfg, boot, time.Now(), counters)
	if err != nil {
		t.Fatal(err)
	}
	if state.Raw.In < uint64(len(payload)) || state.Raw.Out < uint64(len(payload)) {
		t.Fatal("kernel traffic not observed", state.Raw)
	}
	if state.Billed.Out != state.Raw.In+2*state.Raw.Out || state.Billed.In != state.Raw.In {
		t.Fatal("live weighted traffic invariant", state.Raw, state.Billed)
	}
	again, err := meter.Sample(cfg, boot, time.Now(), counters)
	if err != nil {
		t.Fatal(err)
	}
	if again.Billed != state.Billed {
		t.Fatal("same kernel sample counted twice")
	}
}
