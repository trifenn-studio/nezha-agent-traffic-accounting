// SPDX-FileCopyrightText: 2026 Trifenn
// SPDX-License-Identifier: Apache-2.0

package traffic

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/nezhahq/agent/pkg/hostfs"
)

const maxCheckpointSize = 16 << 20

// Meter serializes sampling and holds an exclusive process lock for its ledger.
// Every published increment must have reached a durable checkpoint first.
type Meter struct {
	mu      sync.Mutex
	state   State
	path    string
	unlock  func() error
	persist func(State) (bool, error)
}

func Open(cfg *Config) (*Meter, error) {
	if cfg == nil {
		return nil, errors.New("traffic configuration required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	anchor, err := hostfs.New(cfg.StateFile)
	if err != nil {
		return nil, err
	}
	if err := anchor.EnsureParent(true, 0700); err != nil {
		return nil, errors.Join(err, anchor.Close())
	}
	release, err := lockFile(anchor.NativeDirectory(), anchor.FinalName()+".lock")
	if err != nil {
		return nil, errors.Join(err, anchor.Close())
	}
	closeFiles := func() error { return errors.Join(release(), anchor.Close()) }
	s, err := readState(anchor)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errors.Join(fmt.Errorf("traffic checkpoint unreadable: %w", err), closeFiles())
	}
	if s.Schema != 0 && s.Report != cfg.Report {
		return nil, errors.Join(errors.New("traffic reporting mode differs from checkpoint"), closeFiles())
	}
	return &Meter{state: s, path: cfg.StateFile, unlock: closeFiles, persist: func(s State) (bool, error) {
		return writeState(anchor, s)
	}}, nil
}

func ReadState(path string) (State, error) {
	anchor, err := hostfs.New(path)
	if err != nil {
		return State{}, err
	}
	defer anchor.Close()
	return readState(anchor)
}

func readState(anchor *hostfs.Anchor) (State, error) {
	f, err := anchor.OpenRegular()
	if err != nil {
		var target *hostfs.FinalTargetTypeError
		if errors.As(err, &target) && target.Actual == hostfs.FinalTargetAbsent {
			return State{}, &os.PathError{Op: "open", Path: anchor.TargetPath(), Err: os.ErrNotExist}
		}
		return State{}, err
	}
	defer f.Close()
	limited := &io.LimitedReader{R: f, N: maxCheckpointSize + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var s State
	if err := decoder.Decode(&s); err != nil {
		return State{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return State{}, errors.New("trailing traffic state data")
	}
	if limited.N == 0 {
		return State{}, errors.New("traffic checkpoint exceeds size limit")
	}
	if s.Schema != 1 || (s.Report != "raw" && s.Report != "billing") || s.Boot == "" || s.At.IsZero() || s.Seen == nil || s.Remainder.In >= Scale || s.Remainder.Out >= Scale {
		return State{}, errors.New("invalid traffic checkpoint")
	}
	for id, entry := range s.Seen {
		if entry.Counter.ID != id || id == "" || entry.Counter.Name == "" {
			return State{}, errors.New("invalid traffic checkpoint interface")
		}
		for _, w := range []uint64{entry.Weights.RXToIn, entry.Weights.TXToIn, entry.Weights.RXToOut, entry.Weights.TXToOut} {
			if w > 1000*Scale {
				return State{}, errors.New("invalid checkpoint weight")
			}
		}
	}
	return s, nil
}

func (m *Meter) Sample(cfg *Config, boot string, at time.Time, counters []Counter) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unlock == nil {
		return State{}, errors.New("traffic meter is closed")
	}
	if cfg == nil || cfg.StateFile != m.path {
		return State{}, errors.New("traffic state path cannot change while running")
	}
	next, err := Advance(m.state, cfg, boot, at, counters)
	if err != nil {
		return State{}, err
	}
	committed, err := m.persist(next)
	if committed {
		m.state = next
	}
	if err != nil {
		return State{}, fmt.Errorf("traffic checkpoint failed; reporting held: %w", err)
	}
	// Return detached data so callers cannot mutate the checkpoint.
	return cloneState(next), nil
}

func cloneState(s State) State {
	cloned := s
	if s.Seen != nil {
		cloned.Seen = make(map[string]Entry, len(s.Seen))
		for id, entry := range s.Seen {
			entry.Counter.Lower = slices.Clone(entry.Counter.Lower)
			cloned.Seen[id] = entry
		}
	}
	cloned.Audit.Interfaces = slices.Clone(s.Audit.Interfaces)
	for i := range cloned.Audit.Interfaces {
		observation := &cloned.Audit.Interfaces[i]
		observation.Counter.Lower = slices.Clone(observation.Counter.Lower)
	}
	cloned.Audit.Warnings = slices.Clone(s.Audit.Warnings)
	return cloned
}

func (m *Meter) Checkpoint() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneState(m.state)
}

func (m *Meter) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unlock == nil {
		return nil
	}
	err := m.unlock()
	m.unlock = nil
	return err
}

func writeState(anchor *hostfs.Anchor, s State) (bool, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return false, err
	}
	if len(data)+1 > maxCheckpointSize {
		return false, errors.New("traffic checkpoint exceeds size limit")
	}
	result, err := anchor.AtomicReplace(append(data, '\n'), 0600)
	return result.Committed, err
}
