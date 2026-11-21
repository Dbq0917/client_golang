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

package cardinality_test

import (
	"fmt"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/prometheus/client_golang/exp/cardinality"
	"github.com/prometheus/client_golang/prometheus"
)

// requestPath generates a realistic high-cardinality workload: a handful of
// stable routes, each addressed with per-resource numeric and UUID identifiers
// taken from user input. The number of distinct raw paths is routes times ids.
func requestPath(route, id int) string {
	return fmt.Sprintf(
		"/api/v1/route%d/users/%d/items/%08x-1234-5678-9abc-000000000000",
		route, id, id,
	)
}

// instrument models the instrumentation point: every request increments a
// CounterVec whose "path" label is derived from the request path through the
// supplied constraint.
func instrument(t *testing.T, constraint func(string) string, routes, ids, repeats int) (*prometheus.CounterVec, *prometheus.Registry) {
	t.Helper()

	registry := prometheus.NewRegistry()
	vec := prometheus.V2.NewCounterVec(prometheus.CounterVecOpts{
		CounterOpts: prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests.",
		},
		VariableLabels: prometheus.ConstrainedLabels{
			{Name: "path", Constraint: constraint},
		},
	})
	registry.MustRegister(vec)

	for r := range routes {
		for i := range ids {
			path := requestPath(r, i)
			for range repeats {
				vec.WithLabelValues(path).Inc()
			}
		}
	}
	return vec, registry
}

// seriesCount gathers the registry and counts exported metric samples, which
// equals the number of distinct label combinations held by the vector.
func seriesCount(t *testing.T, registry *prometheus.Registry) int {
	t.Helper()

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather returned error: %v", err)
	}
	count := 0
	for _, family := range families {
		count += len(family.GetMetric())
	}
	return count
}

func TestHighCardinalityStrategies(t *testing.T) {
	const (
		routes           = 5
		ids              = 2000
		repeats          = 3
		limiterBudget    = 100
		rawSeries        = routes * ids
		normalizedSeries = routes
	)

	normalizer := cardinality.NewNormalizer(cardinality.NormalizerOpts{})
	newLimiter := func() *cardinality.Limiter {
		limiter, err := cardinality.NewLimiter(cardinality.LimiterOpts{
			MaxCardinality: limiterBudget,
		})
		if err != nil {
			t.Fatalf("NewLimiter returned error: %v", err)
		}
		return limiter
	}

	tests := []struct {
		name       string
		constraint func(string) string
		wantSeries int
	}{
		{
			name:       "raw path reproduces unbounded cardinality",
			constraint: func(path string) string { return path },
			wantSeries: rawSeries,
		},
		{
			name:       "limiter alone hard-caps the series count",
			constraint: newLimiter().Limit,
			wantSeries: limiterBudget + 1,
		},
		{
			name:       "normalizer alone converges each route to one series",
			constraint: normalizer.Normalize,
			wantSeries: normalizedSeries,
		},
		{
			name: "normalizer plus limiter keeps routes and caps the remainder",
			constraint: func(path string) string {
				return newLimiter().Limit(normalizer.Normalize(path))
			},
			wantSeries: normalizedSeries,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, registry := instrument(t, tc.constraint, routes, ids, repeats)

			if got := seriesCount(t, registry); got != tc.wantSeries {
				t.Errorf("series count = %d, want %d", got, tc.wantSeries)
			}

			// Every observation must still be counted: collapsing label values
			// merges counters but must not drop increments.
			families, err := registry.Gather()
			if err != nil {
				t.Fatalf("Gather returned error: %v", err)
			}
			var total float64
			for _, family := range families {
				for _, metric := range family.GetMetric() {
					total += metric.GetCounter().GetValue()
				}
			}
			if want := float64(routes * ids * repeats); total != want {
				t.Errorf("total counter value = %.0f, want %.0f", total, want)
			}

			// Gather must return a well-formed family for every strategy.
			if len(families) != 1 || families[0].GetName() != "http_requests_total" {
				t.Fatalf("unexpected gathered families: %v", families)
			}
		})
	}
}

// TestGatherScalesWithSeriesCount demonstrates that bounding cardinality also
// bounds Gather work. It measures the same workload raw versus normalized and
// asserts the bounded gather does not exceed the raw one, while documenting the
// actual timings for inspection.
func TestGatherScalesWithSeriesCount(t *testing.T) {
	const (
		routes = 5
		ids    = 5000
	)

	normalizer := cardinality.NewNormalizer(cardinality.NormalizerOpts{})

	_, rawRegistry := instrument(t, func(path string) string { return path }, routes, ids, 1)
	_, boundedRegistry := instrument(t, normalizer.Normalize, routes, ids, 1)

	if raw := seriesCount(t, rawRegistry); raw != routes*ids {
		t.Fatalf("raw series count = %d, want %d", raw, routes*ids)
	}
	if bounded := seriesCount(t, boundedRegistry); bounded != routes {
		t.Fatalf("bounded series count = %d, want %d", bounded, routes)
	}

	// Warm the caches so the comparison reflects steady-state gathering.
	gather := func(registry *prometheus.Registry) []*dto.MetricFamily {
		families, err := registry.Gather()
		if err != nil {
			t.Fatalf("Gather returned error: %v", err)
		}
		return families
	}
	gather(rawRegistry)
	gather(boundedRegistry)

	var rawTotal, boundedTotal int64
	for range 20 {
		rawStart := time.Now()
		gather(rawRegistry)
		rawTotal += time.Since(rawStart).Nanoseconds()

		boundedStart := time.Now()
		gather(boundedRegistry)
		boundedTotal += time.Since(boundedStart).Nanoseconds()
	}

	t.Logf(
		"mean Gather time over 20 runs: raw=%d series %d ns/op, bounded=%d series %d ns/op",
		routes*ids, rawTotal/20, routes, boundedTotal/20,
	)
	if boundedTotal >= rawTotal {
		t.Errorf("bounded Gather (%d ns) was not faster than raw Gather (%d ns)", boundedTotal, rawTotal)
	}
}
