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
	"sync"
	"sync/atomic"
)

// DefaultGroup coordinates snapshots for counters created without an explicit
// group.
var DefaultGroup = NewGroup()

// maxSnapshotAttempts bounds snapshot retries under pathological write
// pressure. A value returned after the limit is still a complete value, and
// the limit only relaxes the all-or-nothing cross-counter guarantee when a
// writer holds a transaction open on every read attempt.
const maxSnapshotAttempts = 1000

// Sample is the read-only view captured for one counter inside a consistent
// [Group] snapshot.
type Sample struct {
	// Counter is the counter the sample was captured from.
	Counter *Counter

	// Value is the complete counter value at the snapshot point.
	Value float64
}

// Snapshot is a point-in-time view of every counter in a [Group].
//
// All values in an accepted snapshot correspond to the same gate epoch: no
// write phase committed while the snapshot was read. Invariants spanning
// several counters of the group (such as a total equal to the sum of shards)
// therefore hold for every accepted snapshot, provided the related updates
// are wrapped in a [Group.Begin] transaction.
//
// Snapshot values are plain float64 copies. Retaining a Snapshot does not
// block writers and does not keep any internal buffer alive.
type Snapshot struct {
	metrics []Sample
}

// Metrics returns the captured samples in the counters' registration order.
//
// The returned slice must not be retained after the Snapshot is no longer
// used; callers that need to keep the data must copy it.
func (s *Snapshot) Metrics() []Sample {
	return s.metrics
}

// Sum adds every captured counter value and returns the result.
func (s *Snapshot) Sum() float64 {
	var total float64
	for _, metric := range s.metrics {
		total += metric.Value
	}
	return total
}

// Group owns a set of counters and coordinates consistent snapshots across
// them.
//
// Two write paths are supported, matched to the two kinds of consistency a
// caller needs:
//
//   - A single-counter mutation ([Counter.Inc] or [Counter.Add]) uses the
//     lock-free [writeGate]. Several such mutations may overlap without
//     serializing one another, and a snapshot never observes a torn
//     individual counter value. The cost is two atomic adds on top of the
//     counter's own atomic value update; no mutex is taken.
//   - A multi-counter transaction opened with [Group.Begin] takes a
//     short-lived mutex so that all of its mutations fall into one gate
//     phase that no other transaction interleaves with. A snapshot then sees
//     either the whole transaction or none of it, which makes cross-counter
//     invariants (for example a total equal to the sum of shards) hold on
//     every scrape.
//
// Readers never block writers. A snapshot waits only for an already closing
// phase to reopen, reads all counters while the gate is open, and accepts the
// read only when the gate epoch is unchanged afterwards.
type Group struct {
	// gate is the multi-writer, lock-free snapshot coordinator.
	gate writeGate

	// txMu serializes explicit multi-counter transactions against each
	// other. Single-counter mutations never take it.
	txMu sync.Mutex

	// txPool reuses transaction objects so that a Begin/Commit pair does not
	// allocate on the hot path.
	txPool sync.Pool

	// mtx guards the counters slice, including registration during an
	// in-flight snapshot.
	mtx      sync.RWMutex
	counters []*Counter
}

// NewGroup creates an empty counter group.
func NewGroup() *Group {
	g := &Group{}
	g.txPool.New = func() any { return &Tx{group: g} }
	return g
}

// add registers a newly created counter with the group.
func (g *Group) add(counter *Counter) {
	g.mtx.Lock()
	g.counters = append(g.counters, counter)
	g.mtx.Unlock()
}

// Begin opens a write transaction spanning several counter mutations.
//
// Every mutation performed before the matching [Tx.Commit] belongs to one
// atomic step as far as snapshots are concerned. A concurrent snapshot sees
// either all of the mutations or none of them.
//
// Mutations are issued through the returned transaction rather than through
// the counters directly, so the gate phase stays open for the whole batch.
// The transaction must be committed exactly once. Use it together with
// defer, for example:
//
//	tx := group.Begin()
//	defer tx.Commit()
//	tx.Add(shard, 1)
//	tx.Inc(total)
func (g *Group) Begin() *Tx {
	g.txMu.Lock()
	g.gate.begin()
	tx := g.txPool.Get().(*Tx)
	tx.done.Store(false)
	return tx
}

// Tx is an open write transaction. See [Group.Begin] for usage.
type Tx struct {
	group *Group
	done  atomic.Bool
}

// Inc increments counter by one within the transaction.
func (tx *Tx) Inc(counter *Counter) {
	tx.Add(counter, 1)
}

// Add adds the given non-negative value to counter within the transaction.
// It panics when value is negative, because a counter must never decrease.
func (tx *Tx) Add(counter *Counter, value float64) {
	if value < 0 {
		panic(ErrCounterDecrease)
	}
	if value == 0 {
		return
	}
	applyAdd(counter, value)
}

// Commit closes the transaction and publishes all mutations performed within
// it as one atomic snapshot step. It is safe to defer; calling it more than
// once has no effect.
func (tx *Tx) Commit() {
	if !tx.done.CompareAndSwap(false, true) {
		return
	}
	tx.group.gate.end()
	tx.group.txMu.Unlock()
	tx.group.txPool.Put(tx)
}

// Snapshot reads every counter of the group at one consistent point in time.
//
// It waits for any closing write phase to reopen, reads all counters while
// the gate is open, and accepts the read only when the gate epoch is
// unchanged afterwards. On a change it retries, so the returned snapshot
// either contains a complete current state or an equivalent state from an
// earlier epoch, but never a partially updated one.
func (g *Group) Snapshot() *Snapshot {
	g.mtx.RLock()
	counters := g.counters

	snapshot := &Snapshot{metrics: make([]Sample, len(counters))}

	for attempt := 0; ; attempt++ {
		// Values read against a closed gate could be mid-phase, so wait for
		// an open epoch before reading.
		start := g.gate.readOpenEpoch()

		for i, counter := range counters {
			snapshot.metrics[i] = Sample{Counter: counter, Value: counter.Value()}
		}

		// Accept only when the gate stayed open at the exact same epoch,
		// meaning no write phase committed while the counters were read.
		if g.gate.stableAt(start) || attempt >= maxSnapshotAttempts {
			g.mtx.RUnlock()
			return snapshot
		}
		runtime.Gosched()
	}
}

// Describe sends the descriptor of every owned counter to ch.
func (g *Group) Describe(ch chan<- *Desc) {
	g.mtx.RLock()
	defer g.mtx.RUnlock()
	for _, counter := range g.counters {
		ch <- counter.Desc()
	}
}

// Collect emits all counters from one consistent snapshot, so every counter
// exposed by a single scrape belongs to the same gate epoch.
func (g *Group) Collect(ch chan<- CollectorMetric) {
	snapshot := g.Snapshot()
	for _, sample := range snapshot.metrics {
		ch <- sample.Counter
	}
}
