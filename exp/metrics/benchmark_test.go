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

package metrics_test

import (
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/exp/metrics"
)

// BenchmarkConsistentCounterInc measures the write path of the snapshot
// consistent counter.
func BenchmarkConsistentCounterInc(b *testing.B) {
	counter := metrics.NewCounter(metrics.CounterOpts{Name: "bench_inc_total"})
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			counter.Inc()
		}
	})
}

// BenchmarkConsistentCounterAdd measures the non-integer Add path, which uses
// a compare-and-swap loop wrapped in one gate phase.
func BenchmarkConsistentCounterAdd(b *testing.B) {
	counter := metrics.NewCounter(metrics.CounterOpts{Name: "bench_add_total"})
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			counter.Add(0.5)
		}
	})
}

// BenchmarkExplicitTransaction measures a two-counter transaction: a shard
// increment and the matching total increment published as one atomic step.
func BenchmarkExplicitTransaction(b *testing.B) {
	group := metrics.NewGroup()
	total := metrics.NewCounter(metrics.CounterOpts{Name: "bench_tx_total", Group: group})
	shard := metrics.NewCounter(metrics.CounterOpts{Name: "bench_tx_shard_total", Group: group})
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			tx := group.Begin()
			tx.Inc(shard)
			tx.Inc(total)
			tx.Commit()
		}
	})
}

// BenchmarkBaselineAtomicInc is the reference write path: one atomic add on a
// bare uint64, matching the cost of a lock-free counter without snapshot
// coordination.
func BenchmarkBaselineAtomicInc(b *testing.B) {
	var value atomic.Uint64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			value.Add(1)
		}
	})
}

// BenchmarkSnapshotRead measures the common, uncontended read path.
func BenchmarkSnapshotRead(b *testing.B) {
	const counters = 100
	group := metrics.NewGroup()
	for range counters {
		metrics.NewCounter(metrics.CounterOpts{Name: "bench_read_total", Group: group})
	}
	b.ResetTimer()
	for range b.N {
		snapshot := group.Snapshot()
		if len(snapshot.Metrics()) != counters {
			b.Fatal("unexpected snapshot size")
		}
	}
}

// BenchmarkSnapshotReadWhileWriting measures scrapes that race with writers.
func BenchmarkSnapshotReadWhileWriting(b *testing.B) {
	const counters = 100
	group := metrics.NewGroup()
	metricCounters := make([]*metrics.Counter, counters)
	for i := range counters {
		metricCounters[i] = metrics.NewCounter(metrics.CounterOpts{
			Name:  "bench_read_write_total",
			Group: group,
		})
	}

	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-stop:
				return
			default:
				for _, counter := range metricCounters {
					counter.Inc()
				}
			}
		}
	}()

	b.ResetTimer()
	for range b.N {
		group.Snapshot()
	}
	b.StopTimer()
	close(stop)
	<-finished
}
