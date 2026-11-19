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

package prometheus

import (
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// exemplarIDs collects the "id" label values of the exemplars currently
// exposed by the histogram, keyed by classic bucket index.
func exemplarIDs(t *testing.T, h *histogram) map[int]string {
	t.Helper()
	var m dto.Metric
	if err := h.Write(&m); err != nil {
		t.Fatal(err)
	}
	ids := map[int]string{}
	for i, b := range m.GetHistogram().GetBucket() {
		if e := b.GetExemplar(); e != nil {
			ids[i] = e.GetLabel()[0].GetValue()
		}
	}
	return ids
}

// TestExemplarLimiterEvictsOldest verifies that once the global limit is
// exceeded, the exemplars with the oldest timestamps are evicted first while
// newer exemplars stay attached to their buckets.
func TestExemplarLimiterEvictsOldest(t *testing.T) {
	limiter := NewExemplarLimiter(2)
	h := NewHistogram(HistogramOpts{
		Name:            "test",
		Help:            "test help",
		Buckets:         []float64{1, 2, 3, 4},
		ExemplarLimiter: limiter,
	}).(*histogram)

	base := time.Now()
	observe := func(v float64, id string, offset time.Duration) {
		h.now = func() time.Time { return base.Add(offset) }
		h.ObserveWithExemplar(v, Labels{"id": id})
	}
	observe(1.5, "1", 0)
	observe(2.5, "2", time.Millisecond)
	observe(3.5, "3", 2*time.Millisecond) // Evicts the exemplar with id "1".

	if got := limiter.Len(); got != 2 {
		t.Errorf("got %d tracked exemplars, want 2.", got)
	}
	want := map[int]string{2: "2", 3: "3"}
	if got := exemplarIDs(t, h); !mapsEqual(got, want) {
		t.Errorf("got exemplars %v, want %v.", got, want)
	}

	// The evicted slot must hold the sentinel, which is neither exposed nor
	// mistaken for a real exemplar, and is replaced by the next observation.
	if got := h.exemplars[1].Load(); got != evictedExemplar {
		t.Errorf("got evicted slot value %v, want the sentinel.", got)
	}
	observe(1.5, "4", 3*time.Millisecond) // Replaces the sentinel, evicts id "2".
	want = map[int]string{1: "4", 3: "3"}
	if got := exemplarIDs(t, h); !mapsEqual(got, want) {
		t.Errorf("got exemplars %v, want %v.", got, want)
	}
}

// TestExemplarLimiterStaleNode verifies that replacing the exemplar of a
// classic bucket does not break the accounting of the limiter: the stale
// node of the replaced exemplar is evicted first and its drop is a no-op.
func TestExemplarLimiterStaleNode(t *testing.T) {
	limiter := NewExemplarLimiter(1)
	h := NewHistogram(HistogramOpts{
		Name:            "test",
		Help:            "test help",
		Buckets:         []float64{1, 2, 3, 4},
		ExemplarLimiter: limiter,
	}).(*histogram)

	base := time.Now()
	observe := func(v float64, id string, offset time.Duration) {
		h.now = func() time.Time { return base.Add(offset) }
		h.ObserveWithExemplar(v, Labels{"id": id})
	}
	observe(1.5, "1", 0)
	observe(1.6, "2", time.Millisecond) // Replaces id "1" in the same bucket.
	// The limit is 1, so the stale node of id "1" is evicted immediately.
	// The slot must still hold the exemplar with id "2".
	want := map[int]string{1: "2"}
	if got := exemplarIDs(t, h); !mapsEqual(got, want) {
		t.Errorf("got exemplars %v, want %v.", got, want)
	}

	observe(3.5, "3", 2*time.Millisecond) // Evicts id "2" from its bucket.
	want = map[int]string{3: "3"}
	if got := exemplarIDs(t, h); !mapsEqual(got, want) {
		t.Errorf("got exemplars %v, want %v.", got, want)
	}
	if got := limiter.Len(); got != 1 {
		t.Errorf("got %d tracked exemplars, want 1.", got)
	}
}

// TestExemplarLimiterNativeStore verifies that the limiter also evicts
// exemplars from the native histogram exemplar store and that the same
// exemplar is tracked only once even though it is kept for both the classic
// buckets and the native histogram.
func TestExemplarLimiterNativeStore(t *testing.T) {
	limiter := NewExemplarLimiter(2)
	h := NewHistogram(HistogramOpts{
		Name:                        "test",
		Help:                        "test help",
		Buckets:                     []float64{1, 2, 3, 4},
		NativeHistogramBucketFactor: 1.1,
		NativeHistogramMaxExemplars: 10,
		ExemplarLimiter:             limiter,
	}).(*histogram)

	base := time.Now()
	for i := range 4 {
		ts := base.Add(time.Duration(i) * time.Millisecond)
		h.now = func() time.Time { return ts }
		h.ObserveWithExemplar(float64(i)+0.5, Labels{"id": "1"})
	}

	if got := limiter.Len(); got != 2 {
		t.Errorf("got %d tracked exemplars, want 2.", got)
	}
	// Only the two most recent exemplars survive, in the native store and in
	// the classic buckets alike.
	compareNativeExemplarValues(t, h.nativeExemplars.exemplars, []float64{2.5, 3.5})
	var m dto.Metric
	if err := h.Write(&m); err != nil {
		t.Fatal(err)
	}
	if got := len(m.GetHistogram().GetExemplars()); got != 2 {
		t.Errorf("got %d native exemplars, want 2.", got)
	}
	var classicExemplars int
	for _, b := range m.GetHistogram().GetBucket() {
		if b.Exemplar != nil {
			classicExemplars++
		}
	}
	if classicExemplars != 2 {
		t.Errorf("got %d classic exemplars, want 2.", classicExemplars)
	}
	// The evictions must not have touched the observation statistics.
	if got := m.GetHistogram().GetSampleCount(); got != 4 {
		t.Errorf("got sample count %d, want 4.", got)
	}
}

// TestExemplarLimiterZero verifies that a limiter with a limit below 1
// retains no exemplars at all while leaving the statistics intact.
func TestExemplarLimiterZero(t *testing.T) {
	h := NewHistogram(HistogramOpts{
		Name:                        "test",
		Help:                        "test help",
		Buckets:                     []float64{1, 2, 3, 4},
		NativeHistogramBucketFactor: 1.1,
		ExemplarLimiter:             NewExemplarLimiter(0),
	}).(*histogram)

	h.ObserveWithExemplar(1.5, Labels{"id": "1"})
	h.ObserveWithExemplar(2.5, Labels{"id": "2"})

	var m dto.Metric
	if err := h.Write(&m); err != nil {
		t.Fatal(err)
	}
	if got := len(m.GetHistogram().GetExemplars()); got != 0 {
		t.Errorf("got %d native exemplars, want 0.", got)
	}
	for i, b := range m.GetHistogram().GetBucket() {
		if b.Exemplar != nil {
			t.Errorf("got exemplar in bucket %d, want none.", i)
		}
	}
	if got := m.GetHistogram().GetSampleCount(); got != 2 {
		t.Errorf("got sample count %d, want 2.", got)
	}
}

func mapsEqual(a, b map[int]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
