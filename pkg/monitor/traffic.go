// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package monitor

import (
	"context"
	"errors"
	"log"
	"math"
	"math/bits"
	"sync"
	"time"

	"github.com/nezhahq/agent/pkg/traffic"
)

var trafficProbe = traffic.Probe

var trafficRuntime struct {
	sync.Mutex
	meter     *traffic.Meter
	lastRaw   traffic.Pair
	lastAt    time.Time
	lastError string
}

// InitTraffic restores the reported totals before the first report can run.
func InitTraffic(cfg *traffic.Config) error {
	if cfg == nil {
		return nil
	}
	trafficRuntime.Lock()
	defer trafficRuntime.Unlock()
	if trafficRuntime.meter != nil {
		return errors.New("traffic meter already initialized")
	}
	meter, err := traffic.Open(cfg)
	if err != nil {
		return err
	}
	trafficRuntime.meter = meter
	trafficRuntime.lastAt = time.Time{}
	trafficRuntime.lastError = ""
	publishTrafficMetrics(meter.Checkpoint().Reported(), traffic.Pair{})
	return nil
}

func trackCustomTraffic(cfg *traffic.Config) {
	trafficRuntime.Lock()
	defer trafficRuntime.Unlock()
	if trafficRuntime.meter == nil {
		holdTrafficMetrics(errors.New("traffic meter is not initialized"))
		return
	}
	boot, counters, err := trafficProbe(context.Background())
	if err != nil {
		holdTrafficMetrics(err)
		return
	}
	now := networkNow()
	state, err := trafficRuntime.meter.Sample(cfg, boot, now, counters)
	if err != nil {
		holdTrafficMetrics(err)
		return
	}
	if trafficRuntime.lastError != "" {
		log.Print("traffic: collection and persistence recovered")
		trafficRuntime.lastError = ""
	}
	var speed traffic.Pair
	if !trafficRuntime.lastAt.IsZero() {
		elapsed := now.Sub(trafficRuntime.lastAt)
		speed.In = bytesPerSecond(state.Raw.In-trafficRuntime.lastRaw.In, elapsed)
		speed.Out = bytesPerSecond(state.Raw.Out-trafficRuntime.lastRaw.Out, elapsed)
	}
	trafficRuntime.lastRaw, trafficRuntime.lastAt = state.Raw, now
	publishTrafficMetrics(state.Reported(), speed)
}

func bytesPerSecond(delta uint64, elapsed time.Duration) uint64 {
	if elapsed <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(delta, uint64(time.Second))
	if hi >= uint64(elapsed) {
		return math.MaxUint64
	}
	rate, _ := bits.Div64(hi, lo, uint64(elapsed))
	return rate
}

func publishTrafficMetrics(total, speed traffic.Pair) {
	metricLock.Lock()
	defer metricLock.Unlock()
	netInTransfer, netOutTransfer = total.In, total.Out
	netInSpeed, netOutSpeed = speed.In, speed.Out
}

func holdTrafficMetrics(err error) {
	message := err.Error()
	if trafficRuntime.lastError != message {
		log.Printf("traffic: %s", message)
		trafficRuntime.lastError = message
	}
	metricLock.Lock()
	netInSpeed, netOutSpeed = 0, 0
	metricLock.Unlock()
}
