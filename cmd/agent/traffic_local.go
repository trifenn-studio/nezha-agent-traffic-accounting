// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/nezhahq/agent/model"
	"github.com/nezhahq/agent/pkg/traffic"
)

func trafficCommand(name, usage string, inspect func(context.Context, *traffic.Config) (any, error)) *cli.Command {
	return &cli.Command{
		Name:  name,
		Usage: usage,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Usage: "配置文件路径"},
		},
		Action: func(c *cli.Context) error {
			path := defaultConfigPath
			for _, scope := range c.Lineage() {
				if value := scope.String("config"); value != "" {
					path = value
					break
				}
			}
			policy, err := model.ReadTrafficConfig(path)
			if err != nil {
				return err
			}
			if policy == nil {
				return errors.New("traffic is not configured")
			}
			result, err := inspect(c.Context, policy)
			if err != nil {
				return err
			}
			return json.NewEncoder(c.App.Writer).Encode(result)
		},
	}
}

func checkTraffic(ctx context.Context, cfg *traffic.Config) (any, error) {
	boot, counters, err := traffic.Probe(ctx)
	if err != nil {
		return nil, err
	}
	state, err := traffic.Advance(traffic.State{}, cfg, boot, time.Now(), counters)
	return state.Audit, err
}

func statusTraffic(_ context.Context, cfg *traffic.Config) (any, error) {
	return traffic.ReadState(cfg.StateFile)
}
