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

package cardinality

import (
	"fmt"
	"sync"
	"testing"
)

func TestNewLimiterDefaults(t *testing.T) {
	limiter, err := NewLimiter(LimiterOpts{})
	if err != nil {
		t.Fatalf("NewLimiter with zero opts returned error: %v", err)
	}
	if limiter.maxCardinality != DefaultMaxCardinality {
		t.Errorf("expected default max cardinality %d, got %d", DefaultMaxCardinality, limiter.maxCardinality)
	}
	if limiter.overflowValue != DefaultOverflowValue {
		t.Errorf("expected default overflow value %q, got %q", DefaultOverflowValue, limiter.overflowValue)
	}
}

func TestNewLimiterValidation(t *testing.T) {
	if _, err := NewLimiter(LimiterOpts{MaxCardinality: -1}); err == nil {
		t.Error("expected error for negative MaxCardinality, got nil")
	}
}

func TestLimiterRetainsValuesWithinBudget(t *testing.T) {
	const budget = 3
	limiter, err := NewLimiter(LimiterOpts{MaxCardinality: budget, OverflowValue: "overflow"})
	if err != nil {
		t.Fatalf("NewLimiter returned error: %v", err)
	}

	for i := range budget {
		value := fmt.Sprintf("path-%d", i)
		if got := limiter.Limit(value); got != value {
			t.Errorf("value %d: expected %q retained, got %q", i, value, got)
		}
	}
	if got := limiter.Len(); got != budget {
		t.Errorf("expected Len %d, got %d", budget, got)
	}

	// Repeated observations of retained values stay stable and do not grow
	// cardinality.
	for range 10 {
		if got := limiter.Limit("path-1"); got != "path-1" {
			t.Errorf("expected retained value to stay stable, got %q", got)
		}
	}
	if got := limiter.Len(); got != budget {
		t.Errorf("expected Len to stay %d after repeats, got %d", budget, got)
	}
}

func TestLimiterCollapsesValuesBeyondBudget(t *testing.T) {
	const budget = 2
	var rejected []string
	limiter, err := NewLimiter(LimiterOpts{
		MaxCardinality: budget,
		OverflowValue:  "overflow",
		OnOverflow: func(value string) {
			rejected = append(rejected, value)
		},
	})
	if err != nil {
		t.Fatalf("NewLimiter returned error: %v", err)
	}

	inputs := []string{"a", "b", "c", "d", "c", "b"}
	expected := []string{"a", "b", "overflow", "overflow", "overflow", "b"}
	for i, input := range inputs {
		if got := limiter.Limit(input); got != expected[i] {
			t.Errorf("input %q: expected %q, got %q", input, expected[i], got)
		}
	}

	// Only "c" and "d" are distinct overflows; the repeated "c" must not be
	// counted or reported twice.
	if got := limiter.OverflowCount(); got != 2 {
		t.Errorf("expected overflow count 2, got %d", got)
	}
	if len(rejected) != 2 {
		t.Errorf("expected 2 overflow callbacks, got %d (%v)", len(rejected), rejected)
	}
	if got := limiter.Len(); got != budget {
		t.Errorf("expected Len to stay at budget %d, got %d", budget, got)
	}
}

func TestLimiterReset(t *testing.T) {
	limiter, err := NewLimiter(LimiterOpts{MaxCardinality: 1, OverflowValue: "overflow"})
	if err != nil {
		t.Fatalf("NewLimiter returned error: %v", err)
	}

	limiter.Limit("a")
	if got := limiter.Limit("b"); got != "overflow" {
		t.Errorf("expected overflow before reset, got %q", got)
	}

	limiter.Reset()
	if got := limiter.Len(); got != 0 {
		t.Errorf("expected Len 0 after reset, got %d", got)
	}
	if got := limiter.OverflowCount(); got != 0 {
		t.Errorf("expected overflow count 0 after reset, got %d", got)
	}
	if got := limiter.Limit("b"); got != "b" {
		t.Errorf("expected budget restored after reset, got %q", got)
	}
}

func TestLimiterConcurrent(t *testing.T) {
	const (
		budget     = 50
		goroutines = 16
		iterations = 500
	)
	limiter, err := NewLimiter(LimiterOpts{MaxCardinality: budget, OverflowValue: "overflow"})
	if err != nil {
		t.Fatalf("NewLimiter returned error: %v", err)
	}

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(goroutine int) {
			defer wg.Done()
			for i := range iterations {
				// Each goroutine spans more distinct values than the budget,
				// forcing contention on both fast and slow paths.
				value := fmt.Sprintf("g%d-v%d", goroutine, i%100)
				got := limiter.Limit(value)
				if got != value && got != "overflow" {
					t.Errorf("unexpected output %q for %q", got, value)
				}
			}
		}(g)
	}
	wg.Wait()

	// The cardinality invariant must hold regardless of interleaving.
	if got := limiter.Len(); got > budget {
		t.Errorf("cardinality exceeded budget: Len %d > %d", got, budget)
	}
	if got := limiter.OverflowCount(); got < 0 {
		t.Errorf("unexpected negative overflow count: %d", got)
	}
	totalDistinct := goroutines * 100
	if got := limiter.Len() + int(limiter.OverflowCount()); got != totalDistinct {
		t.Errorf("retained %d plus overflow %d does not match total distinct values", got, limiter.OverflowCount())
	}
	if got := limiter.OverflowCount(); got != int64(totalDistinct-budget) {
		t.Errorf("expected %d distinct overflows, got %d", totalDistinct-budget, got)
	}
}
