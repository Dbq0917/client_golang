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

import "testing"

func TestNormalizerNormalize(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "empty path",
			path: "",
			want: "",
		},
		{
			name: "root path",
			path: "/",
			want: "/",
		},
		{
			name: "static route stays untouched",
			path: "/api/v1/users/profile",
			want: "/api/v1/users/profile",
		},
		{
			name: "numeric user id collapses",
			path: "/api/v1/users/1234",
			want: "/api/v1/users/:id",
		},
		{
			name: "multiple numeric ids collapse independently",
			path: "/api/v1/users/1234/orders/98765",
			want: "/api/v1/users/:id/orders/:id",
		},
		{
			name: "uuid collapses but route structure remains",
			path: "/users/8f3a9c2e-7d5b-4f1a-9b2c-6e8d0f1a2b3c",
			want: "/users/:id",
		},
		{
			name: "long hex hash collapses",
			path: "/files/da39a3ee5e6b4b0d3255bfef95601890afd80709",
			want: "/files/:id",
		},
		{
			name: "short hex word is retained",
			path: "/api/v1/face/dead",
			want: "/api/v1/face/dead",
		},
		{
			name: "query string is dropped",
			path: "/api/v1/users/1234?token=abcdef&ref=ghi",
			want: "/api/v1/users/:id",
		},
		{
			name: "trailing slash is preserved",
			path: "/api/v1/users/1234/",
			want: "/api/v1/users/:id/",
		},
		{
			name: "mixed static and identifier segments",
			path: "/shops/42/products/sku-77/reviews",
			want: "/shops/:id/products/sku-77/reviews",
		},
	}

	normalizer := NewNormalizer(NormalizerOpts{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizer.Normalize(tc.path); got != tc.want {
				t.Errorf("Normalize(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestNormalizerCustomSentinelAndMatchers(t *testing.T) {
	normalizer := NewNormalizer(NormalizerOpts{
		IDSentinel: "{resource}",
		Matchers: []SegmentMatcher{
			func(segment string) bool {
				return segment == "me"
			},
		},
	})

	if got := normalizer.Normalize("/users/me/orders/42"); got != "/users/{resource}/orders/{resource}" {
		t.Errorf("custom sentinel and matcher not applied, got %q", got)
	}
}

func TestNormalizerRoutesStayDistinguishable(t *testing.T) {
	normalizer := NewNormalizer(NormalizerOpts{})

	// Different resource routes must not merge even though their identifiers
	// collapse to the same sentinel.
	first := normalizer.Normalize("/users/1/orders/2")
	second := normalizer.Normalize("/shops/1/orders/2")
	if first == second {
		t.Errorf("distinct routes collapsed to same value: %q", first)
	}

	// All instances of one route must converge to a single template.
	instanceA := normalizer.Normalize("/users/100/orders/200")
	instanceB := normalizer.Normalize("/users/999/orders/888")
	if instanceA != instanceB {
		t.Errorf("same route produced different templates: %q vs %q", instanceA, instanceB)
	}
}
