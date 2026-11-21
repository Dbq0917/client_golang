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
	"errors"
	"math"
	"sync/atomic"

	dto "github.com/prometheus/client_model/go"
)

// ErrCounterDecrease is returned or used to panic when a counter would be
// decreased by a negative increment.
var ErrCounterDecrease = errors.New("counter cannot decrease in value")

// Counter is a monotonically increasing numerical value whose increments stay
// cheap while every read can be coordinated into a consistent snapshot.
//
// The value is tracked in two fields. valInt counts integer increments with a
// single atomic add, while valBits accumulates non-integer floats with a
// compare-and-swap loop. Both fields are individual atomic words, so a reader
// always observes complete values and never a partially published increment.
// Cross-counter consistency is provided by the group gate.
//
// A Counter is safe for concurrent use by multiple writer and reader
// goroutines. The zero Counter is not usable; create one with [NewCounter].
type Counter struct {
	// Both atomic fields have to go first in the struct to guarantee
	// alignment for atomic operations.
	// https://pkg.go.dev/sync/atomic#pkg-note-BUG
	valInt  atomic.Uint64
	valBits atomic.Uint64

	// group is the coordinator whose gate marks the write phase that contains
	// every mutation of this counter.
	group *Group

	desc       *Desc
	labelPairs []*dto.LabelPair
}

// NewCounter creates a Counter from the provided options.
//
// The returned counter registers itself with the [Group] stored in Opts, or
// with [DefaultGroup] when Opts.Group is nil. NewCounter panics when the
// options describe an invalid metric.
func NewCounter(opts CounterOpts) *Counter {
	desc := newDesc(opts)
	counter := &Counter{
		group:      opts.group(),
		desc:       desc,
		labelPairs: desc.constLabelPairs,
	}
	counter.group.add(counter)
	return counter
}

// Inc increments the counter by one.
func (c *Counter) Inc() {
	c.Add(1)
}

// Add adds the given non-negative value to the counter. It panics when value
// is negative, because a counter must never decrease.
func (c *Counter) Add(value float64) {
	if value < 0 {
		panic(ErrCounterDecrease)
	}
	if value == 0 {
		// A no-op increment changes neither value nor gate and therefore
		// cannot disturb an in-flight snapshot.
		return
	}

	// Integer increments use the contention-friendly atomic add fast path;
	// only non-integer floats fall back to the compare-and-swap loop.
	c.mutate(func() { applyAdd(c, value) })
}

// mutate runs valueUpdate inside one gate phase.
func (c *Counter) mutate(valueUpdate func()) {
	end := c.group.gate.begin()
	valueUpdate()
	end()
}

// applyAdd performs the value mutation without touching the gate. It is
// shared by the single-counter path, which wraps it in its own gate phase,
// and by an explicit [Tx], whose gate phase already spans the batch.
func applyAdd(c *Counter, value float64) {
	intValue := uint64(value)
	if float64(intValue) == value {
		c.valInt.Add(intValue)
		return
	}
	addFloat(&c.valBits, value)
}

// addFloat atomically adds the non-integer value to the float64 represented
// by bits, retrying on contention until the compare-and-swap succeeds.
func addFloat(bits *atomic.Uint64, value float64) {
	for {
		oldBits := bits.Load()
		newBits := math.Float64bits(math.Float64frombits(oldBits) + value)
		if bits.CompareAndSwap(oldBits, newBits) {
			return
		}
	}
}

// Value returns the current counter value.
//
// The returned float64 is always a complete, never partially updated value.
// For cross-counter invariants, prefer reading values through
// [Group.Snapshot] instead of calling Value on several counters directly.
func (c *Counter) Value() float64 {
	return math.Float64frombits(c.valBits.Load()) + float64(c.valInt.Load())
}

// Desc returns the descriptor of the counter.
func (c *Counter) Desc() *Desc {
	return c.desc
}

// Write encodes the counter into the provided Protocol Buffer metric.
//
// Write implements the Metric interface understood by the stable prometheus
// registry, so counters registered there are scraped like any built-in
// counter.
func (c *Counter) Write(out *dto.Metric) error {
	out.Label = c.labelPairs
	out.Counter = &dto.Counter{Value: ptr(c.Value())}
	return nil
}

// ptr returns a pointer to value. It avoids allocating through
// proto.Float64 in packages that do not depend on the stable prometheus
// package.
func ptr(value float64) *float64 {
	return &value
}
