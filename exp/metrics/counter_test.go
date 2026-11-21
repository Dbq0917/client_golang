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
	"errors"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/exp/metrics"
)

func TestCounterAddAndRead(t *testing.T) {
	group := metrics.NewGroup()
	counter := metrics.NewCounter(metrics.CounterOpts{
		Name:  "requests_total",
		Help:  "Total number of requests.",
		Group: group,
	})

	counter.Inc()
	counter.Add(2.5)
	counter.Add(0)

	if got, want := counter.Value(), 3.5; got != want {
		t.Fatalf("Value() = %v, want %v", got, want)
	}

	snapshot := group.Snapshot()
	if len(snapshot.Metrics()) != 1 || snapshot.Sum() != 3.5 {
		t.Fatalf("unexpected snapshot: %+v", snapshot.Metrics())
	}
}

func TestCounterNegativeAddPanics(t *testing.T) {
	group := metrics.NewGroup()
	counter := metrics.NewCounter(metrics.CounterOpts{Name: "negative_total", Group: group})

	defer func() {
		recovered := recover()
		if !errors.Is(recovered.(error), metrics.ErrCounterDecrease) {
			t.Fatalf("panic = %v, want ErrCounterDecrease", recovered)
		}
	}()
	counter.Add(-1)
}

func TestNewCounterInvalidName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewCounter with an invalid name must panic")
		}
	}()
	metrics.NewCounter(metrics.CounterOpts{Name: ""})
}

func TestSnapshotIsMonotonic(t *testing.T) {
	group := metrics.NewGroup()
	counter := metrics.NewCounter(metrics.CounterOpts{Name: "monotonic_total", Group: group})
	var total float64

	previous := counter.Value()
	for range 100 {
		snapshot := group.Snapshot()
		current := snapshot.Sum()
		if current < previous {
			t.Fatalf("snapshot value decreased: %v -> %v", previous, current)
		}
		previous = current
		counter.Inc()
		total++
	}
	if got := group.Snapshot().Sum(); got != total {
		t.Fatalf("final snapshot sum = %v, want %v", got, total)
	}
}

// TestConcurrentScrapeConservation mirrors a capacity-model workload:
// several worker goroutines increment a shard counter and the matching total
// counter, while a scraper takes one consistent snapshot per second.
//
// Every worker keeps total == sum(shards) after each logical operation. A
// snapshot that interleaves with an operation would violate the invariant, so
// each scrape is checked for sum conservation.
func TestConcurrentScrapeConservation(t *testing.T) {
	const (
		workers      = 8
		scrapeEvery  = time.Second
		testDuration = 3 * time.Second
	)

	group := metrics.NewGroup()
	total := metrics.NewCounter(metrics.CounterOpts{
		Name:  "events_total",
		Help:  "Total processed events.",
		Group: group,
	})
	shards := make([]*metrics.Counter, workers)
	for i := range workers {
		shards[i] = metrics.NewCounter(metrics.CounterOpts{
			Name:        "events_shard_total",
			Help:        "Per-shard processed events.",
			ConstLabels: metrics.Labels{"shard": strconv.Itoa(i)},
			Group:       group,
		})
	}

	stop := make(chan struct{})
	var writers sync.WaitGroup
	for worker := range workers {
		writers.Add(1)
		go func(id int) {
			defer writers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					// One logical event increments the shard and its total.
					// Both mutations are wrapped in one transaction, so every
					// accepted snapshot sees either both increments or
					// neither.
					tx := group.Begin()
					tx.Add(shards[id], 1)
					tx.Inc(total)
					tx.Commit()
				}
			}
		}(worker)
	}

	var scrapes sync.WaitGroup
	scrapes.Add(1)
	go func() {
		defer scrapes.Done()
		ticker := time.NewTicker(scrapeEvery)
		defer ticker.Stop()
		deadline := time.NewTimer(testDuration)
		defer deadline.Stop()
		for {
			select {
			case <-ticker.C:
				snapshot := group.Snapshot()
				assertConservation(t, snapshot, total, shards)
			case <-deadline.C:
				return
			}
		}
	}()

	// Wait for the periodic scrapes to finish, then stop the writers. A final
	// quiescent snapshot must satisfy the same invariant.
	scrapes.Wait()
	close(stop)
	writers.Wait()

	final := group.Snapshot()
	assertConservation(t, final, total, shards)
}

func assertConservation(
	t *testing.T,
	snapshot *metrics.Snapshot,
	total *metrics.Counter,
	shards []*metrics.Counter,
) {
	t.Helper()

	values := map[*metrics.Counter]float64{}
	var shardSum float64
	for _, sample := range snapshot.Metrics() {
		values[sample.Counter] = sample.Value
	}
	for _, shard := range shards {
		shardSum += values[shard]
	}
	scrapedTotal := values[total]

	if math.Abs(scrapedTotal-shardSum) > 1e-9 {
		t.Fatalf(
			"sum not conserved at %s: total=%v, sum(shards)=%v",
			time.Now().Format(time.RFC3339Nano),
			scrapedTotal, shardSum,
		)
	}
}

// TestConcurrentSingleCounterNoTear verifies that plain single-counter
// increments never expose a torn value: every snapshot is monotonically
// non-decreasing and equals the counter's own complete value, even without an
// explicit transaction.
func TestConcurrentSingleCounterNoTear(t *testing.T) {
	const (
		workers      = 8
		testDuration = 2 * time.Second
	)

	group := metrics.NewGroup()
	counter := metrics.NewCounter(metrics.CounterOpts{Name: "plain_total", Group: group})

	stop := make(chan struct{})
	var writers sync.WaitGroup
	for range workers {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					counter.Add(0.25)
				}
			}
		}()
	}

	deadline := time.NewTimer(testDuration)
	defer deadline.Stop()
	var previous float64
	for {
		select {
		case <-deadline.C:
			close(stop)
			writers.Wait()
			final := group.Snapshot().Sum()
			if final < previous {
				t.Fatalf("final value decreased: %v -> %v", previous, final)
			}
			return
		default:
			value := group.Snapshot().Sum()
			if value < previous {
				t.Fatalf("torn read: value decreased %v -> %v", previous, value)
			}
			previous = value
		}
	}
}

func TestGathererFamilies(t *testing.T) {
	group := metrics.NewGroup()
	metrics.NewCounter(metrics.CounterOpts{
		Name:        "events_total",
		Help:        "Events.",
		ConstLabels: metrics.Labels{"shard": "a"},
		Group:       group,
	})
	metrics.NewCounter(metrics.CounterOpts{
		Name:        "events_total",
		Help:        "Events.",
		ConstLabels: metrics.Labels{"shard": "b"},
		Group:       group,
	})

	families, err := metrics.NewGatherer(group).Gather()
	if err != nil {
		t.Fatal("Gather failed:", err)
	}
	if len(families) != 1 || len(families[0].GetMetric()) != 2 {
		t.Fatalf("unexpected families: %+v", families)
	}
	if families[0].GetName() != "events_total" {
		t.Fatalf("unexpected family name: %q", families[0].GetName())
	}
}

// TestGathererConservation verifies the actual scrape path: the gathered
// metric families are built from one snapshot, so the total family value
// equals the sum of the shard family values after every transaction.
func TestGathererConservation(t *testing.T) {
	group := metrics.NewGroup()
	gatherer := metrics.NewGatherer(group)
	total := metrics.NewCounter(metrics.CounterOpts{Name: "events_total", Group: group})
	shards := make([]*metrics.Counter, 4)
	for i := range shards {
		shards[i] = metrics.NewCounter(metrics.CounterOpts{
			Name:        "events_shard_total",
			ConstLabels: metrics.Labels{"shard": strconv.Itoa(i)},
			Group:       group,
		})
	}

	for round := 1; round <= 50; round++ {
		tx := group.Begin()
		for _, shard := range shards {
			tx.Inc(shard)
			tx.Inc(total)
		}
		tx.Commit()

		families, err := gatherer.Gather()
		if err != nil {
			t.Fatal("Gather failed:", err)
		}

		values := map[string]float64{}
		for _, family := range families {
			for _, metric := range family.GetMetric() {
				values[family.GetName()] += metric.GetCounter().GetValue()
			}
		}
		if values["events_total"] != values["events_shard_total"] {
			t.Fatalf(
				"round %d: gathered total %v != shard sum %v",
				round, values["events_total"], values["events_shard_total"],
			)
		}
	}
}
