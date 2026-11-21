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

package prometheus_test

import (
	"bytes"
	"fmt"
	"runtime"
	"strconv"
	"testing"

	"github.com/prometheus/common/expfmt"

	"github.com/prometheus/client_golang/prometheus"
)

// exemplarCardinalityObservations is the number of observations, each carrying
// a unique trace ID, pumped through a single metric in the regression tests
// below. It is deliberately large enough that an unbounded implementation
// would consume hundreds of megabytes of heap.
const exemplarCardinalityObservations = 100_000

// retainedHeapKiB returns the live heap size in KiB after forcing a GC cycle
// so that garbage left behind by replaced exemplars is not counted.
func retainedHeapKiB() uint64 {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc / 1024
}

func traceIDLabels(i int) prometheus.Labels {
	return prometheus.Labels{
		"trace_id": fmt.Sprintf("0000000000000000000000000000%06x", i),
	}
}

// countSeriesAndExemplars gathers the registry and counts the number of
// exposed metric series and the number of exemplars attached to them.
func countSeriesAndExemplars(t *testing.T, registry *prometheus.Registry) (series, exemplars int) {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		series += len(family.Metric)
		for _, metric := range family.Metric {
			if metric.Counter != nil && metric.Counter.Exemplar != nil {
				exemplars++
			}
			if metric.Histogram != nil {
				for _, bucket := range metric.Histogram.Bucket {
					if bucket.Exemplar != nil {
						exemplars++
					}
				}
				exemplars += len(metric.Histogram.Exemplars)
			}
		}
	}
	return series, exemplars
}

// openMetricsScrapeSize encodes the gathered families in the OpenMetrics text
// format and returns the resulting scrape payload size in bytes.
func openMetricsScrapeSize(t *testing.T, registry *prometheus.Registry) int {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	encoder := expfmt.NewEncoder(&buffer, expfmt.FmtOpenMetrics_1_0_0)
	for _, family := range families {
		if err := encoder.Encode(family); err != nil {
			t.Fatal(err)
		}
	}
	if closer, ok := encoder.(expfmt.Closer); ok {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buffer.Len()
}

// TestExemplarDoesNotInflateSeriesAndHeap pumps a unique, high-cardinality
// trace ID through the exemplar channel of every metric type that supports
// exemplars. Exemplars must be retained in bounded, per-metric storage slots,
// so the number of exposed time series stays constant, the number of
// exemplars stays bounded by the metric configuration, and the retained heap
// does not grow with the number of observations.
func TestExemplarDoesNotInflateSeriesAndHeap(t *testing.T) {
	tests := []struct {
		name             string
		wantExemplarsMax int
		observe          func(metric prometheus.Collector, i int)
		newMetric        func() prometheus.Collector
	}{
		{
			name:             "counter keeps a single exemplar",
			wantExemplarsMax: 1,
			newMetric: func() prometheus.Collector {
				return prometheus.NewCounter(prometheus.CounterOpts{
					Name: "exemplar_counter_total",
					Help: "Counter with exemplars.",
				})
			},
			observe: func(metric prometheus.Collector, i int) {
				metric.(prometheus.ExemplarAdder).AddWithExemplar(1, traceIDLabels(i))
			},
		},
		{
			name:             "classic histogram keeps one exemplar per bucket",
			wantExemplarsMax: 6,
			newMetric: func() prometheus.Collector {
				return prometheus.NewHistogram(prometheus.HistogramOpts{
					Name:    "exemplar_histogram_seconds",
					Help:    "Histogram with exemplars.",
					Buckets: prometheus.ExponentialBuckets(0.1, 1.5, 5),
				})
			},
			observe: func(metric prometheus.Collector, i int) {
				metric.(prometheus.ExemplarObserver).ObserveWithExemplar(0.15, traceIDLabels(i))
			},
		},
		{
			name: "native histogram caps retained exemplars",
			// NativeHistogramMaxExemplars bounds the sparse exemplar list,
			// while the classic bucket path keeps one additional exemplar.
			wantExemplarsMax: 101,
			newMetric: func() prometheus.Collector {
				return prometheus.NewHistogram(prometheus.HistogramOpts{
					Name:                        "exemplar_native_histogram_seconds",
					Help:                        "Native histogram with exemplars.",
					NativeHistogramBucketFactor: 1.1,
					NativeHistogramMaxExemplars: 100,
				})
			},
			observe: func(metric prometheus.Collector, i int) {
				value := 0.15 + float64(i%50)*0.01
				metric.(prometheus.ExemplarObserver).ObserveWithExemplar(value, traceIDLabels(i))
			},
		},
	}

	// A single metric instance must not retain additional memory as the trace
	// ID cardinality grows. The comparison is done within one subtest between
	// a small and a large batch. A generous allowance keeps the assertion
	// stable across Go versions while an unbounded retention regression would
	// exceed it by orders of magnitude.
	const warmupObservations, maxRetainedHeapKiB = 100, 4 * 1024

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := prometheus.NewRegistry()
			metric := test.newMetric()
			registry.MustRegister(metric)

			for i := 0; i < warmupObservations; i++ {
				test.observe(metric, i)
			}
			smallScrapeSize := openMetricsScrapeSize(t, registry)
			before := retainedHeapKiB()
			for i := warmupObservations; i < exemplarCardinalityObservations; i++ {
				test.observe(metric, i)
			}
			after := retainedHeapKiB()
			largeScrapeSize := openMetricsScrapeSize(t, registry)

			series, exemplars := countSeriesAndExemplars(t, registry)
			if series != 1 {
				t.Errorf("expected exactly one time series regardless of trace ID cardinality, got %d.", series)
			}
			if exemplars > test.wantExemplarsMax {
				t.Errorf("expected at most %d retained exemplars, got %d.", test.wantExemplarsMax, exemplars)
			}
			if exemplars == 0 {
				t.Fatal("expected trace IDs to remain traceable through at least one retained exemplar.")
			}
			if delta := int64(after) - int64(before); delta > maxRetainedHeapKiB {
				t.Errorf("retained heap grew by %d KiB after %d observations, want at most %d KiB.",
					delta, exemplarCardinalityObservations, maxRetainedHeapKiB)
			}
			// The payload may vary by a few bytes because exemplar timestamps
			// and values have different text widths, but it must not grow with
			// the number of distinct trace IDs processed.
			if sizeDelta := largeScrapeSize - smallScrapeSize; sizeDelta > 64 {
				t.Errorf("scrape payload grew by %d bytes (%d to %d) as trace ID cardinality grew.",
					sizeDelta, smallScrapeSize, largeScrapeSize)
			}

			t.Logf("observations=%d series=%d exemplars=%d heap_delta_KiB=%d scrape_bytes=%d",
				exemplarCardinalityObservations, series, exemplars, int64(after)-int64(before), largeScrapeSize)
		})
	}
}

// TestMetricLabelCardinalityContrast documents the failure mode that using a
// trace ID as a regular metric label creates one time series per request. It
// serves as a contrast against the bounded exemplar behavior and guards
// against the storage approach being silently swapped for label-based
// retention.
func TestMetricLabelCardinalityContrast(t *testing.T) {
	registry := prometheus.NewRegistry()
	histogramVec := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "label_cardinality_histogram_seconds",
			Help:    "Histogram keyed by a trace ID label.",
			Buckets: prometheus.ExponentialBuckets(0.1, 1.5, 5),
		},
		[]string{"trace_id"},
	)
	registry.MustRegister(histogramVec)

	for i := 0; i < exemplarCardinalityObservations; i++ {
		histogramVec.WithLabelValues(strconv.Itoa(i)).Observe(0.15)
	}

	series, exemplars := countSeriesAndExemplars(t, registry)
	if series != exemplarCardinalityObservations {
		t.Fatalf("expected one series per trace ID, got %d series for %d trace IDs.",
			series, exemplarCardinalityObservations)
	}
	if exemplars != 0 {
		t.Errorf("expected no exemplars for label-based trace IDs, got %d.", exemplars)
	}
}

// BenchmarkExemplarWithUniqueTraceID measures the per-observation cost of
// attaching a unique trace ID through the exemplar channel. Each iteration
// allocates a new exemplar, but storage is bounded by a single slot, so the
// benchmark numbers stay flat instead of accumulating across iterations.
func BenchmarkExemplarWithUniqueTraceID(b *testing.B) {
	registry := prometheus.NewRegistry()
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "benchmark_exemplar_counter_total",
		Help: "Counter benchmarked with unique exemplar trace IDs.",
	})
	registry.MustRegister(counter)
	adder := counter.(prometheus.ExemplarAdder)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		adder.AddWithExemplar(1, prometheus.Labels{
			"trace_id": fmt.Sprintf("%032x", i),
		})
	}
}

// BenchmarkMetricLabelWithUniqueTraceID is the contrast benchmark for using a
// unique trace ID as a regular metric label, which retains one series per
// iteration and therefore grows memory without bound.
func BenchmarkMetricLabelWithUniqueTraceID(b *testing.B) {
	registry := prometheus.NewRegistry()
	counterVec := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "benchmark_label_counter_total",
			Help: "Counter benchmarked with unique trace ID labels.",
		},
		[]string{"trace_id"},
	)
	registry.MustRegister(counterVec)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counterVec.WithLabelValues(fmt.Sprintf("%032x", i)).Inc()
	}
}
