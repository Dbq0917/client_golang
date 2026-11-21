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

import "strings"

// DefaultIDSentinel is the placeholder substituted for identifier-like path
// segments when NormalizerOpts.IDSentinel is left at its zero value.
const DefaultIDSentinel = ":id"

// SegmentMatcher reports whether a single URL path segment behaves as a
// resource identifier and should be replaced by the configured sentinel. The
// segment never contains a slash.
type SegmentMatcher func(segment string) bool

// NormalizerOpts configures a Normalizer. The zero value is usable:
// NewNormalizer applies sensible defaults that recognize numeric IDs, UUIDs,
// and long hexadecimal hashes.
type NormalizerOpts struct {
	// IDSentinel replaces every identifier-like path segment. An empty value
	// selects DefaultIDSentinel.
	IDSentinel string

	// Matchers holds additional segment matchers applied in order before the
	// built-in heuristics. A segment is replaced when any matcher reports
	// true. Leave nil to rely on the built-in heuristics alone.
	Matchers []SegmentMatcher
}

// Normalizer rewrites user-supplied request paths into stable route templates
// by collapsing identifier-like segments into a shared sentinel.
//
// For example, with the default options:
//
//	"/api/v1/users/1234/orders/8f3a9c2e-7d5b-4f1a-9b2c-6e8d0f1a2b3c"
//
// becomes
//
//	"/api/v1/users/:id/orders/:id"
//
// Collapsing IDs before the value reaches a metric vector removes the
// per-resource dimension of cardinality while preserving the route structure,
// so distinct endpoints remain distinguishable. A Normalizer is stateless and
// safe for concurrent use.
type Normalizer struct {
	sentinel string
	matchers []SegmentMatcher
}

// NewNormalizer validates opts and returns a ready-to-use Normalizer.
func NewNormalizer(opts NormalizerOpts) *Normalizer {
	if opts.IDSentinel == "" {
		opts.IDSentinel = DefaultIDSentinel
	}
	return &Normalizer{
		sentinel: opts.IDSentinel,
		matchers: opts.Matchers,
	}
}

// Normalize rewrites path into a route template. The query string, if any, is
// dropped: query parameters are an independent and typically unbounded source
// of cardinality and must not be used as label values.
//
// Normalize has the signature func(string) string, so a Normalizer method value
// can be used directly as a prometheus.LabelConstraint, typically chained
// before a Limiter:
//
//	normalizer := cardinality.NewNormalizer(cardinality.NormalizerOpts{})
//	limiter, _ := cardinality.NewLimiter(cardinality.LimiterOpts{})
//	constraint := func(path string) string {
//		return limiter.Limit(normalizer.Normalize(path))
//	}
func (n *Normalizer) Normalize(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if path == "" || path == "/" {
		return path
	}

	// First scan the segments using slicing only, which allocates nothing.
	// Paths without identifier-like segments are returned untouched and stay
	// allocation-free on the hot path.
	if !n.hasIdentifierSegment(path) {
		return path
	}

	// At least one segment needs rewriting, so build the template once.
	var builder strings.Builder
	builder.Grow(len(path))
	rest := path
	for {
		slash := strings.IndexByte(rest, '/')
		var segment string
		if slash < 0 {
			segment, rest = rest, ""
		} else {
			segment, rest = rest[:slash], rest[slash+1:]
		}
		if segment != "" && n.isIdentifier(segment) {
			builder.WriteString(n.sentinel)
		} else {
			builder.WriteString(segment)
		}
		if slash < 0 {
			break
		}
		builder.WriteByte('/')
	}
	return builder.String()
}

// hasIdentifierSegment reports whether any non-empty segment of path would be
// collapsed. It only slices the input and therefore does not allocate.
func (n *Normalizer) hasIdentifierSegment(path string) bool {
	rest := path
	for rest != "" {
		slash := strings.IndexByte(rest, '/')
		var segment string
		if slash < 0 {
			segment, rest = rest, ""
		} else {
			segment, rest = rest[:slash], rest[slash+1:]
		}
		if segment != "" && n.isIdentifier(segment) {
			return true
		}
	}
	return false
}

// isIdentifier reports whether segment should be collapsed. Custom matchers run
// first so callers can extend or override the built-in heuristics.
func (n *Normalizer) isIdentifier(segment string) bool {
	for _, match := range n.matchers {
		if match(segment) {
			return true
		}
	}
	return looksLikeID(segment)
}

// looksLikeID applies the built-in identifier heuristics: pure numeric IDs,
// UUIDs (with or without dashes), and hexadecimal hashes of at least twelve
// characters. Short lowercase words such as "users" never match, so route
// segments keep their distinguishing power.
func looksLikeID(segment string) bool {
	if isAllDigits(segment) {
		return true
	}

	hexDigits := 0
	hasDash := false
	for _, r := range segment {
		switch {
		case r >= '0' && r <= '9':
			hexDigits++
		case r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
			hexDigits++
		case r == '-':
			hasDash = true
		default:
			// Any non-hex, non-dash character means this segment is an
			// ordinary word, not an identifier.
			return false
		}
	}

	if hasDash {
		// UUIDs and dashed identifiers are only accepted when they carry
		// enough hexadecimal entropy to avoid matching ordinary hyphenated
		// words, which cannot consist solely of hex characters anyway.
		return hexDigits >= 8
	}
	return hexDigits >= 12
}

// isAllDigits reports whether segment consists entirely of decimal digits and
// is non-empty.
func isAllDigits(segment string) bool {
	if segment == "" {
		return false
	}
	for _, r := range segment {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
