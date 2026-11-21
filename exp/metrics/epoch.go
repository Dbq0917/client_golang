// Copyright 2026 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metrics

import (
	"runtime"
	"sync/atomic"
)

// Gate states, stored in the lowest bit of writeGate.state.
const (
	// epochOpen means no phase is in flight and snapshots may read.
	epochOpen uint64 = 0

	// epochClosed means at least one writer is inside a phase that has not
	// fully drained, so a snapshot must not read against it.
	epochClosed uint64 = 1
)

// writeGate coordinates many concurrent writers and lock-free readers.
//
// Both begin and end perform one atomic add, so every write phase advances
// state by two. The lowest bit therefore alternates between even (open, no
// phase in flight) and odd (closed, at least one phase in flight):
//
//   - begin on an even state flips it to odd and opens the phase. A begin on
//     an already odd state merely joins the overlapping phase.
//   - end on an odd state flips it back to even when this was the last
//     writer. An end while overlapping writers remain stays within the
//     phase.
//
// The state is monotonic and never returns to zero after the first phase,
// but the low bit always reflects whether the gate is currently open.
//
// Readers never block writers. They wait only for an already closing phase
// to reopen, read all values while the gate is open, and accept the read
// only when the full state value is unchanged afterwards. Hence a reader
// observes either every mutation committed before it read the open epoch or
// none of the mutations that committed afterwards, but never a partial
// phase.
type writeGate struct {
	// state has to go first in the struct to guarantee alignment for 64-bit
	// atomic operations on 32-bit platforms.
	// https://pkg.go.dev/sync/atomic#pkg-note-BUG
	state atomic.Uint64
}

// begin enters a write phase and returns a function that must be called once
// when the phase's mutations are complete.
//
// The cost is one atomic add on entry and one atomic add on exit; no writer
// ever spins waiting for another writer.
func (g *writeGate) begin() func() {
	g.state.Add(1)
	return g.end
}

// end leaves a write phase. The last writer of a phase flips the gate back to
// an open state.
func (g *writeGate) end() {
	g.state.Add(1)
}

// readOpenEpoch blocks until the gate is open and returns the open epoch
// value. The returned value must be compared with the gate state after the
// protected values have been read via [writeGate.stableAt].
func (g *writeGate) readOpenEpoch() uint64 {
	for {
		state := g.state.Load()
		if state&1 == epochOpen {
			return state
		}
		runtime.Gosched()
	}
}

// stableAt reports whether the gate stayed open at epoch throughout a read.
// A different state means a write phase committed while reading, in which
// case the caller must retry.
func (g *writeGate) stableAt(epoch uint64) bool {
	return g.state.Load() == epoch
}
