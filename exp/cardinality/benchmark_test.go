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
	"testing"
)

// BenchmarkLimiterKnownValue measures the hot path once the value set has
// stabilized, which is the steady state of a long-running instrumented service.
func BenchmarkLimiterKnownValue(b *testing.B) {
	limiter, err := NewLimiter(LimiterOpts{MaxCardinality: 1024})
	if err != nil {
		b.Fatalf("NewLimiter returned error: %v", err)
	}
	for i := range 1024 {
		limiter.Limit(fmt.Sprintf("/api/v1/users/%d", i))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = limiter.Limit("/api/v1/users/512")
	}
}

// BenchmarkLimiterOverflow measures the steady-state path for values that
// exceed the budget: the lookup misses twice but no new map entry is created.
func BenchmarkLimiterOverflow(b *testing.B) {
	limiter, err := NewLimiter(LimiterOpts{MaxCardinality: 64})
	if err != nil {
		b.Fatalf("NewLimiter returned error: %v", err)
	}
	for i := range 64 {
		limiter.Limit(fmt.Sprintf("known-%d", i))
	}
	// Prime the overflow set so repeated rejected values take the same path as
	// a stable deployment where the overflow bucket already exists.
	limiter.Limit("rejected-0")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = limiter.Limit("rejected-0")
	}
}

// BenchmarkLimiterUniqueOverflow measures genuinely unseen overflow values,
// exercising the write-locked slow path that records each distinct rejection.
func BenchmarkLimiterUniqueOverflow(b *testing.B) {
	limiter, err := NewLimiter(LimiterOpts{MaxCardinality: 64})
	if err != nil {
		b.Fatalf("NewLimiter returned error: %v", err)
	}
	for i := range 64 {
		limiter.Limit(fmt.Sprintf("known-%d", i))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = limiter.Limit(fmt.Sprintf("unseen-%d", i))
	}
}

// BenchmarkNormalizer measures the rewrite cost on a realistic path carrying a
// numeric and a UUID identifier.
func BenchmarkNormalizer(b *testing.B) {
	normalizer := NewNormalizer(NormalizerOpts{})
	path := "/api/v1/users/12345/orders/8f3a9c2e-7d5b-4f1a-9b2c-6e8d0f1a2b3c"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = normalizer.Normalize(path)
	}
}

// BenchmarkNormalizerStatic measures the common case of a path without any
// identifier-like segment, which must be returned with minimal work.
func BenchmarkNormalizerStatic(b *testing.B) {
	normalizer := NewNormalizer(NormalizerOpts{})
	path := "/api/v1/users/profile/settings"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = normalizer.Normalize(path)
	}
}

// BenchmarkChained measures the recommended composition of normalization
// followed by cardinality limiting on the stabilized hot path.
func BenchmarkChained(b *testing.B) {
	normalizer := NewNormalizer(NormalizerOpts{})
	limiter, err := NewLimiter(LimiterOpts{MaxCardinality: 1024})
	if err != nil {
		b.Fatalf("NewLimiter returned error: %v", err)
	}
	constrain := func(path string) string {
		return limiter.Limit(normalizer.Normalize(path))
	}
	for i := range 1024 {
		constrain(fmt.Sprintf("/api/v1/route%d/users/123", i))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = constrain("/api/v1/route512/users/456")
	}
}
