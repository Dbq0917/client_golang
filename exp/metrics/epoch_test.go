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
	"sync"
	"testing"
)

func TestWriteGateQuiescentIsOpen(t *testing.T) {
	var gate writeGate
	if state := gate.state.Load(); state&1 != epochOpen {
		t.Fatalf("quiescent gate state = %d, want open", state)
	}

	end := gate.begin()
	if state := gate.state.Load(); state&1 != epochClosed {
		t.Fatalf("open phase state = %d, want closed", state)
	}
	end()
	if state := gate.state.Load(); state&1 != epochOpen {
		t.Fatalf("drained phase state = %d, want open", state)
	}
}

func TestWriteGateQuiescesAfterOverlappingWriters(t *testing.T) {
	var (
		gate    writeGate
		wg      sync.WaitGroup
		ends    []func()
		endsMu  sync.Mutex
		workers = 64
	)

	// Open many overlapping phases, then close them all. The gate must
	// always return to an open state with no residual in-flight writers.
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			end := gate.begin()
			endsMu.Lock()
			ends = append(ends, end)
			endsMu.Unlock()
		}()
	}
	wg.Wait()

	for _, end := range ends {
		end()
	}

	state := gate.state.Load()
	if state&1 != epochOpen {
		t.Fatalf("gate state after drain = %d, want open", state)
	}
	// Every one of the workers performed a balanced begin/end pair, so the
	// total number of state increments is even and the drained gate reports
	// an open state.
	if state != uint64(2*workers) {
		t.Fatalf("gate state after drain = %d, want %d", state, 2*workers)
	}
}

func TestWriteGateDetectsCommittedPhase(t *testing.T) {
	var gate writeGate

	epoch := gate.readOpenEpoch()
	end := gate.begin()
	end()

	if gate.stableAt(epoch) {
		t.Fatal("gate must reject a snapshot whose epoch changed during the read")
	}

	// A fresh read after the phase drains is stable again.
	epoch = gate.readOpenEpoch()
	if !gate.stableAt(epoch) {
		t.Fatal("gate must accept a snapshot with no intervening phase")
	}
}
