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
	"container/heap"
	"sync"
	"sync/atomic"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// evictedExemplar is a sentinel stored in a classic bucket exemplar slot in
// place of an exemplar that has been evicted by an ExemplarLimiter. It is
// never exposed in gathered metrics and is replaced by the next exemplar
// observed for the bucket.
var evictedExemplar = &dto.Exemplar{}

// ExemplarLimiter caps the total number of exemplars retained in memory
// across all histograms that share it. Without a limiter, every histogram
// keeps its own exemplars independently (one per classic bucket, up to
// NativeHistogramMaxExemplars per native histogram), so the total exemplar
// memory footprint grows linearly with the number of series. Under
// high-cardinality labels this can dominate the heap. A shared
// ExemplarLimiter turns that into a hard global bound: once the number of
// tracked exemplars exceeds the limit, the exemplars with the oldest
// timestamps are evicted first, no matter which histogram they belong to.
//
// Eviction only removes exemplars. Bucket counts, sums, and all other
// observations statistics are never affected, and an exemplar that survives
// eviction stays attached to the bucket it was observed for.
//
// The zero value is not usable; create a limiter with NewExemplarLimiter and
// share it between histograms via HistogramOpts.ExemplarLimiter. A
// ExemplarLimiter is safe for concurrent use.
type ExemplarLimiter struct {
	mu sync.Mutex
	// max is the maximum number of exemplars tracked at any time.
	max int
	// heap holds the tracked exemplars ordered by timestamp, oldest first.
	// An exemplar stays tracked until it is evicted, so heap.Len() bounds
	// the total number of exemplar objects retained in memory.
	heap exemplarNodeHeap
}

// NewExemplarLimiter returns an ExemplarLimiter that retains at most
// maxExemplars exemplars across all histograms using it, evicting the
// oldest exemplars first once the limit is exceeded. If maxExemplars is
// below 1, no exemplars are retained at all.
func NewExemplarLimiter(maxExemplars int) *ExemplarLimiter {
	if maxExemplars < 0 {
		maxExemplars = 0
	}
	return &ExemplarLimiter{max: maxExemplars}
}

// Len returns the number of exemplars currently tracked by the limiter. It
// is intended for introspection and testing.
func (l *ExemplarLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.heap.Len()
}

// exemplarNode tracks a single exemplar held by a histogram so that an
// ExemplarLimiter can evict it later. The same exemplar may be referenced by
// a classic bucket slot and by a native histogram exemplar store at the same
// time, in which case both are cleared on eviction.
type exemplarNode struct {
	// timestamp is the exemplar timestamp used as the eviction order key.
	timestamp time.Time
	// exemplar is the tracked exemplar.
	exemplar *dto.Exemplar
	// slot is the classic bucket slot holding the exemplar, nil if the
	// exemplar is not kept for a classic bucket.
	slot *atomic.Value
	// store is the native histogram exemplar store holding the exemplar,
	// nil if the exemplar is not kept for a native histogram.
	store *nativeExemplars
}

// track registers n with the limiter and evicts the oldest exemplars until
// the number of tracked exemplars is within the configured limit.
func (l *ExemplarLimiter) track(n *exemplarNode) {
	l.mu.Lock()
	heap.Push(&l.heap, n)
	var evicted []*exemplarNode
	for l.heap.Len() > l.max {
		evicted = append(evicted, heap.Pop(&l.heap).(*exemplarNode))
	}
	l.mu.Unlock()

	// Drop the evicted exemplars only after releasing l.mu. Dropping from a
	// native exemplar store acquires the store's own mutex, and the store
	// may call back into track while holding it, so dropping under l.mu
	// could deadlock.
	for _, n := range evicted {
		n.drop()
	}
}

// drop removes the tracked exemplar from its owners. It is a no-op for
// owners that no longer hold this exact exemplar, e.g. because a newer
// exemplar has already replaced it.
func (n *exemplarNode) drop() {
	if n.slot != nil {
		// Only clear the slot if it still holds this exact exemplar. A
		// concurrent observation may have replaced it already. A sentinel
		// is stored instead of nil because atomic.Value does not allow
		// storing nil.
		n.slot.CompareAndSwap(n.exemplar, evictedExemplar)
	}
	if n.store != nil {
		n.store.remove(n.exemplar)
	}
}

// exemplarNodeHeap is a min-heap of exemplar nodes ordered by exemplar
// timestamp, so that the oldest exemplar is evicted first. It implements
// heap.Interface.
type exemplarNodeHeap []*exemplarNode

func (h exemplarNodeHeap) Len() int { return len(h) }

func (h exemplarNodeHeap) Less(i, j int) bool { return h[i].timestamp.Before(h[j].timestamp) }

func (h exemplarNodeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *exemplarNodeHeap) Push(x any) {
	*h = append(*h, x.(*exemplarNode))
}

func (h *exemplarNodeHeap) Pop() any {
	old := *h
	n := len(old)
	node := old[n-1]
	old[n-1] = nil // Don't retain the popped node.
	*h = old[:n-1]
	return node
}
