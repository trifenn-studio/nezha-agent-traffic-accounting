//go:build !linux

// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package main

func startTrafficReload(string) func() { return func() {} }
