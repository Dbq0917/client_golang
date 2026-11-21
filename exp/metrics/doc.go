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

// Package metrics provides experimental metric primitives whose scraped
// values are coordinated into consistent snapshots.
//
// The primitives in this package are experimental and may change or be
// removed in the future. They complement the stable prometheus package rather
// than replacing it.
//
// # Motivation
//
// A Counter in the stable prometheus package performs every increment as a
// single atomic operation, so an individual counter never exposes a torn
// value. However, a scrape reads several independent metrics one after
// another while writer goroutines keep incrementing them. Two scraped
// metrics that are expected to satisfy an invariant (for example a total
// counter equal to the sum of per-shard counters) can therefore be observed
// at different logical points in time. The scraped snapshot may then violate
// the invariant, which makes capacity models built from such snapshots
// self-contradictory.
//
// # Snapshot consistency
//
// Every mutation is wrapped in a write phase marked by a seqlock-style gate
// on the [Group]. A scrape waits for any closing phase to reopen, reads all
// counters while the gate is open, and then checks that the gate epoch did
// not change. It is only accepted when the epoch is unchanged, in which case
// every reported value belongs to the same point in time and cross-metric
// invariants hold. If a phase committed in between, the scrape is retried
// and falls back to an earlier complete snapshot instead of returning a
// partially updated one.
//
// Multiple counters that must change together can be updated inside one
// explicit transaction opened with [Group.Begin]. A shard increment and the
// matching total increment wrapped this way are published atomically with
// respect to every snapshot.
//
// # Write path cost
//
// Integer increments update one uint64 word with a single atomic add, while
// non-integer floats use a compare-and-swap loop on a second word, exactly
// like a lock-free counter. Marking the surrounding phase costs one
// additional atomic add on entry and one on exit. No mutex or
// reader-writer barrier is taken on the single-counter write path, and
// readers never block writers. The incremental cost of snapshot consistency
// is therefore two atomic adds per mutation and one gate load per counter
// read in the common, uncontended case.
package metrics
