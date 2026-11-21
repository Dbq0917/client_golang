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

// Package cardinality provides tools for bounding the cardinality of label
// values that are derived from untrusted or unbounded input, such as HTTP
// request paths.
//
// The package is experimental and lives in the exp module so that its API can
// evolve without affecting the stability guarantees of the root module.
package cardinality

import (
	"errors"
	"sync"
	"sync/atomic"
)

// DefaultMaxCardinality is the upper bound applied when LimiterOpts.MaxCardinality
// is left at its zero value.
const DefaultMaxCardinality = 1000

// DefaultOverflowValue is the label value used to replace any input that does
// not fit within the cardinality budget when LimiterOpts.OverflowValue is left
// at its zero value.
const DefaultOverflowValue = "other"

var errZeroMaxCardinality = errors.New("cardinality: MaxCardinality must be greater than zero")

// LimiterOpts configures a Limiter. The zero value is not usable directly;
// construct a Limiter with NewLimiter, which applies sensible defaults.
type LimiterOpts struct {
	// MaxCardinality bounds the number of distinct input values that are
	// retained as their own label values. Values observed after the budget is
	// exhausted are collapsed into OverflowValue. A zero value selects
	// DefaultMaxCardinality.
	MaxCardinality int

	// OverflowValue is the label value returned for every input that does not
	// fit within MaxCardinality. It must not be empty. A zero value selects
	// DefaultOverflowValue.
	OverflowValue string

	// OnOverflow, when non-nil, is invoked once for every distinct input value
	// that is redirected to OverflowValue. Repeated observations of the same
	// value do not trigger the callback again. It is called while the Limiter
	// is locked, so implementations must not call back into the Limiter.
	OnOverflow func(rejectedValue string)
}

// Limiter bounds the number of distinct output values produced from an
// unbounded stream of inputs. It is safe for concurrent use.
//
// The first MaxCardinality distinct input values are retained verbatim. Every
// subsequent distinct value is mapped to the shared OverflowValue, so that the
// total number of output values never exceeds MaxCardinality plus one. This
// first-seen policy is deterministic and allocation-free on the hot path: an
// already known value is resolved with a single read-lock and a map lookup.
//
// A Limiter owns its internal map and never retains the input string beyond
// the map lookup. Callers remain free to reuse or mutate any buffers they
// pass in after Limit returns.
type Limiter struct {
	maxCardinality int
	overflowValue  string
	onOverflow     func(string)

	// overflowCount counts distinct input values redirected to OverflowValue.
	// It is updated on the locked slow path and read without a lock.
	overflowCount atomic.Int64

	mu       sync.RWMutex
	known    map[string]struct{}
	overflow map[string]struct{}
}

// NewLimiter validates opts and returns a ready-to-use Limiter.
func NewLimiter(opts LimiterOpts) (*Limiter, error) {
	if opts.MaxCardinality == 0 {
		opts.MaxCardinality = DefaultMaxCardinality
	}
	if opts.MaxCardinality < 0 {
		return nil, errZeroMaxCardinality
	}
	if opts.OverflowValue == "" {
		opts.OverflowValue = DefaultOverflowValue
	}
	if opts.OnOverflow == nil {
		// Keep a non-nil no-op so the hot path skips a nil check.
		opts.OnOverflow = func(string) {}
	}

	return &Limiter{
		maxCardinality: opts.MaxCardinality,
		overflowValue:  opts.OverflowValue,
		onOverflow:     opts.OnOverflow,
		known:          make(map[string]struct{}, opts.MaxCardinality),
		overflow:       make(map[string]struct{}),
	}, nil
}

// Limit maps an input value to a cardinality-bounded label value. Known values
// are returned unchanged; unknown values are retained while budget remains and
// otherwise collapsed into OverflowValue.
//
// Limit has the signature func(string) string, so a Limiter method value can be
// used directly as a prometheus.LabelConstraint:
//
//	limiter, _ := cardinality.NewLimiter(cardinality.LimiterOpts{MaxCardinality: 100})
//	prometheus.V2.NewCounterVec(prometheus.CounterVecOpts{
//		VariableLabels: prometheus.ConstrainedLabels{
//			{Name: "path", Constraint: limiter.Limit},
//		},
//	})
func (l *Limiter) Limit(value string) string {
	l.mu.RLock()
	_, ok := l.known[value]
	l.mu.RUnlock()
	if ok {
		return value
	}
	return l.admit(value)
}

// admit is the locked slow path for a value that the read-lock lookup missed.
func (l *Limiter) admit(value string) string {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Re-check under the write lock because another goroutine may have won the
	// race and admitted the same value while this goroutine waited.
	if _, ok := l.known[value]; ok {
		return value
	}
	if _, ok := l.overflow[value]; ok {
		return l.overflowValue
	}
	if len(l.known) >= l.maxCardinality {
		// Record distinct overflow values so repeated observations are neither
		// counted nor reported through OnOverflow a second time.
		l.overflow[value] = struct{}{}
		l.overflowCount.Add(1)
		l.onOverflow(value)
		return l.overflowValue
	}

	// Storing the string copies its header into the map; the caller's
	// underlying buffer is not retained.
	l.known[value] = struct{}{}
	return value
}

// Len returns the number of distinct input values currently retained. It does
// not count the shared OverflowValue.
func (l *Limiter) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.known)
}

// OverflowCount returns the number of distinct input values that were
// redirected to OverflowValue. Repeated observations of the same value count
// once.
func (l *Limiter) OverflowCount() int64 {
	return l.overflowCount.Load()
}

// Reset clears the retained values and the overflow counter, restoring the
// Limiter to its initial budget.
func (l *Limiter) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	clear(l.known)
	clear(l.overflow)
	l.overflowCount.Store(0)
}
