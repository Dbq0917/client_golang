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

package metrics

// Labels represents a collection of label name to value mappings.
type Labels map[string]string

// CounterOpts bundles the options to create a [Counter].
//
// Name is mandatory. The remaining fields are optional. When Group is nil,
// the counter joins [DefaultGroup].
type CounterOpts struct {
	// Namespace and Subsystem are prefixes joined to Name with underscores
	// to form the fully-qualified metric name.
	Namespace string
	Subsystem string

	// Name is the mandatory metric name.
	Name string

	// Help describes the metric and is exposed alongside its samples.
	Help string

	// ConstLabels are fixed labels attached to every sample of the metric.
	// The map is read during construction and is not retained, so callers
	// may reuse it afterwards.
	ConstLabels Labels

	// Group coordinates consistent snapshots for the counter. When nil,
	// [DefaultGroup] is used.
	Group *Group
}

// group returns the explicitly configured group or the default group.
func (opts CounterOpts) group() *Group {
	if opts.Group != nil {
		return opts.Group
	}
	return DefaultGroup
}
