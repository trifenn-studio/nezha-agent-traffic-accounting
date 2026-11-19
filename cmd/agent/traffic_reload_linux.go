// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/nezhahq/agent/model"
)

func startTrafficReload(path string) func() {
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	stopped := make(chan struct{})
	signal.Notify(signals, syscall.SIGHUP)
	go func() {
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			case <-signals:
				if err := reloadLocalTraffic(path); err != nil {
					log.Printf("traffic reload rejected: %v", err)
				} else {
					log.Print("traffic policy reloaded for the next sampling boundary")
				}
			}
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
		<-stopped
	}
}

func reloadLocalTraffic(path string) error {
	reloadMu.Lock()
	defer reloadMu.Unlock()
	if reloadTimer != nil {
		return errors.New("remote configuration reload pending")
	}
	policy, err := model.ReadTrafficConfig(path)
	if err != nil {
		return err
	}
	current := loadRuntimeConfig()
	if current.Traffic == nil || policy == nil {
		return errors.New("enabling or disabling traffic requires a restart")
	}
	if current.Traffic.StateFile != policy.StateFile || current.Traffic.Report != policy.Report {
		return errors.New("SIGHUP can only change traffic include, exclude and weights")
	}
	cfg := *current
	cfg.Traffic = policy
	if err := model.ValidateConfig(&cfg, false); err != nil {
		return err
	}
	// Report ticks read the latest snapshot without reconnecting.
	publishRuntimeConfig(cfg)
	return nil
}
