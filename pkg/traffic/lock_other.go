//go:build !linux && !darwin && !freebsd

// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package traffic

import (
	"errors"
	"os"
)

func lockFile(*os.File, string) (func() error, error) {
	return nil, errors.New("custom traffic ledger is supported on Linux only")
}
