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
	"fmt"
	"math"
	"runtime"
	"sync"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/prometheus/client_golang/prometheus"
)

// countExemplars returns the total number of exemplars exposed by the
// histogram, counting both classic bucket exemplars and native histogram
// exemplars.
func countExemplars(his *dto.Histogram) int {
	n := len(his.GetExemplars())
	for _, b := range his.GetBucket() {
		if b.Exemplar != nil {
			n++
		}
	}
	return n
}

// gatherHistograms gathers all metrics from reg and returns their histograms.
func gatherHistograms(tb testing.TB, reg *prometheus.Registry) []*dto.Histogram {
	tb.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		tb.Fatal(err)
	}
	var his []*dto.Histogram
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			his = append(his, m.GetHistogram())
		}
	}
	return his
}

// bucketQuantile estimates the q-quantile from classic buckets using linear
// interpolation within a bucket, mirroring PromQL's histogram_quantile. It
// is a test helper and assumes all observations fall into finite buckets.
func bucketQuantile(q float64, his *dto.Histogram) float64 {
	buckets := his.GetBucket()
	count := his.GetSampleCount()
	if count == 0 || len(buckets) == 0 {
		return math.NaN()
	}
	rank := q * float64(count)
	lower := 0.0
	var prevCum uint64
	for _, b := range buckets {
		upper := b.GetUpperBound()
		if math.IsInf(upper, +1) {
			break
		}
		cum := b.GetCumulativeCount()
		if float64(cum) >= rank {
			if cum == prevCum {
				return upper
			}
			return lower + (upper-lower)*(rank-float64(prevCum))/float64(cum-prevCum)
		}
		lower = upper
		prevCum = cum
	}
	return math.NaN()
}

// observeWithExemplars observes n deterministic values in [0, 10) into each
// series of the vector, attaching a unique exemplar to every observation.
func observeWithExemplars(vec *prometheus.HistogramVec, series, n int) {
	for s := range series {
		h := vec.WithLabelValues(fmt.Sprintf("series-%d", s))
		for i := range n {
			v := float64((s*37+i*13)%1000) / 100
			h.(prometheus.ExemplarObserver).ObserveWithExemplar(v, prometheus.Labels{
				"trace_id": fmt.Sprintf("%032x", s*n+i),
			})
		}
	}
}

func TestExemplarLimiterGlobalCap(t *testing.T) {
	const (
		series       = 200
		observations = 20
		maxExemplars = 50
	)
	tcs := []struct {
		name string
		opts prometheus.HistogramOpts
	}{
		{
			name: "classic histogram",
			opts: prometheus.HistogramOpts{
				Name:    "classic_duration_seconds",
				Help:    "Classic histogram with exemplars.",
				Buckets: []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			},
		},
		{
			name: "native histogram",
			opts: prometheus.HistogramOpts{
				Name:                        "native_duration_seconds",
				Help:                        "Native histogram with exemplars.",
				Buckets:                     []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
				NativeHistogramBucketFactor: 1.1,
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			limiter := prometheus.NewExemplarLimiter(maxExemplars)
			tc.opts.ExemplarLimiter = limiter
			reg := prometheus.NewRegistry()
			vec := prometheus.NewHistogramVec(tc.opts, []string{"id"})
			reg.MustRegister(vec)

			observeWithExemplars(vec, series, observations)

			var totalExemplars int
			for _, his := range gatherHistograms(t, reg) {
				totalExemplars += countExemplars(his)
			}
			if totalExemplars > maxExemplars {
				t.Errorf("got %d exemplars, want at most %d.", totalExemplars, maxExemplars)
			}
			if got := limiter.Len(); got > maxExemplars {
				t.Errorf("limiter tracks %d exemplars, want at most %d.", got, maxExemplars)
			}
		})
	}
}

// TestExemplarLimiterStatsUnaffected verifies that enabling the limiter
// changes neither the observation statistics nor the quantiles derived from
// the classic buckets, and that every surviving exemplar stays attached to
// the bucket its value belongs to.
func TestExemplarLimiterStatsUnaffected(t *testing.T) {
	const (
		series       = 100
		observations = 50
		maxExemplars = 40
	)
	newVec := func(limiter *prometheus.ExemplarLimiter) (*prometheus.Registry, *prometheus.HistogramVec) {
		reg := prometheus.NewRegistry()
		vec := prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:            "request_duration_seconds",
			Help:            "Request duration.",
			Buckets:         []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			ExemplarLimiter: limiter,
		}, []string{"id"})
		reg.MustRegister(vec)
		return reg, vec
	}

	regUnlimited, vecUnlimited := newVec(nil)
	observeWithExemplars(vecUnlimited, series, observations)
	regLimited, vecLimited := newVec(prometheus.NewExemplarLimiter(maxExemplars))
	observeWithExemplars(vecLimited, series, observations)

	unlimited := gatherHistograms(t, regUnlimited)
	limited := gatherHistograms(t, regLimited)
	if len(unlimited) != len(limited) {
		t.Fatalf("got %d limited series, want %d.", len(limited), len(unlimited))
	}
	for i, want := range unlimited {
		got := limited[i]
		if got.GetSampleCount() != want.GetSampleCount() {
			t.Errorf("series %d: got sample count %d, want %d.", i, got.GetSampleCount(), want.GetSampleCount())
		}
		if got.GetSampleSum() != want.GetSampleSum() {
			t.Errorf("series %d: got sample sum %f, want %f.", i, got.GetSampleSum(), want.GetSampleSum())
		}
		if len(got.GetBucket()) != len(want.GetBucket()) {
			t.Fatalf("series %d: got %d buckets, want %d.", i, len(got.GetBucket()), len(want.GetBucket()))
		}
		for j, wb := range want.GetBucket() {
			gb := got.GetBucket()[j]
			if gb.GetCumulativeCount() != wb.GetCumulativeCount() {
				t.Errorf("series %d bucket %d: got cumulative count %d, want %d.", i, j, gb.GetCumulativeCount(), wb.GetCumulativeCount())
			}
		}
		for _, q := range []float64{0.5, 0.9, 0.99} {
			gotQ, wantQ := bucketQuantile(q, got), bucketQuantile(q, want)
			if gotQ != wantQ {
				t.Errorf("series %d: got p%g %f, want %f.", i, q*100, gotQ, wantQ)
			}
		}
	}

	// Every surviving exemplar must be attached to the bucket its observed
	// value falls into, and the global limit must hold.
	var totalExemplars int
	for _, his := range limited {
		for j, b := range his.GetBucket() {
			e := b.Exemplar
			if e == nil {
				continue
			}
			totalExemplars++
			upper := b.GetUpperBound()
			lower := 0.0
			if j > 0 {
				lower = his.GetBucket()[j-1].GetUpperBound()
			}
			if v := e.GetValue(); v > upper || v <= lower {
				t.Errorf("exemplar value %f attached to bucket (%g, %g].", v, lower, upper)
			}
		}
	}
	if totalExemplars > maxExemplars {
		t.Errorf("got %d exemplars, want at most %d.", totalExemplars, maxExemplars)
	}
}

// TestExemplarLimiterConcurrent exercises concurrent observations, exemplar
// eviction, and gathers to surface data races. Run with -race.
func TestExemplarLimiterConcurrent(t *testing.T) {
	const (
		workers      = 8
		series       = 50
		observations = 200
		maxExemplars = 100
	)
	limiter := prometheus.NewExemplarLimiter(maxExemplars)
	reg := prometheus.NewRegistry()
	vec := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:                        "concurrent_duration_seconds",
		Help:                        "Concurrently observed histogram.",
		Buckets:                     []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		NativeHistogramBucketFactor: 1.1,
		ExemplarLimiter:             limiter,
	}, []string{"id"})
	reg.MustRegister(vec)

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for s := range series {
				h := vec.WithLabelValues(fmt.Sprintf("series-%d", s))
				v := float64((w*series+s)%1000) / 100
				for i := range observations {
					h.(prometheus.ExemplarObserver).ObserveWithExemplar(v, prometheus.Labels{
						"trace_id": fmt.Sprintf("%032x", w*series*observations+s*observations+i),
					})
				}
			}
		}(w)
	}
	// Gather concurrently with the observations.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := reg.Gather(); err != nil {
					panic(err)
				}
			}
		}
	}()
	wg.Wait()
	close(stop)

	var totalExemplars int
	var totalCount uint64
	for _, his := range gatherHistograms(t, reg) {
		totalExemplars += countExemplars(his)
		totalCount += his.GetSampleCount()
	}
	if totalExemplars > maxExemplars {
		t.Errorf("got %d exemplars, want at most %d.", totalExemplars, maxExemplars)
	}
	if want := uint64(workers * series * observations); totalCount != want {
		t.Errorf("got total sample count %d, want %d.", totalCount, want)
	}
}

// BenchmarkExemplarLimiterHighCardinality measures the heap retained by
// exemplars under high-cardinality labels, with and without a global
// exemplar limit, and reports the quantiles derived from the classic buckets
// to demonstrate that capping exemplars does not distort the statistics.
// Run with: go test -run=NONE -bench=ExemplarLimiterHighCardinality -benchtime=1x ./prometheus
func BenchmarkExemplarLimiterHighCardinality(b *testing.B) {
	const (
		series       = 20000
		observations = 25
		maxExemplars = 10000
	)
	tcs := []struct {
		name    string
		limiter *prometheus.ExemplarLimiter
	}{
		{name: "no_limit"},
		{name: "limited", limiter: prometheus.NewExemplarLimiter(maxExemplars)},
	}
	for _, tc := range tcs {
		b.Run(tc.name, func(b *testing.B) {
			reg := prometheus.NewRegistry()
			vec := prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:            "request_duration_seconds",
				Help:            "Request duration.",
				Buckets:         []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
				ExemplarLimiter: tc.limiter,
			}, []string{"id"})
			reg.MustRegister(vec)

			for b.Loop() {
				observeWithExemplars(vec, series, observations)
			}

			runtime.GC()
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			b.ReportMetric(float64(ms.HeapAlloc)/(1<<20), "heap-MB")

			his := gatherHistograms(b, reg)
			if len(his) == 0 {
				b.Fatal("no series gathered.")
			}
			// Report the quantiles of a representative series. All series
			// observe the same value pattern, so they are identical.
			b.ReportMetric(bucketQuantile(0.5, his[0]), "p50-s")
			b.ReportMetric(bucketQuantile(0.99, his[0]), "p99-s")
			var totalExemplars int
			for _, h := range his {
				totalExemplars += countExemplars(h)
			}
			b.ReportMetric(float64(totalExemplars), "exemplars")
		})
	}
}
