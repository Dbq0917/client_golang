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

import (
	"sort"

	dto "github.com/prometheus/client_model/go"
)

// Metric is the interface implemented by every scrapable metric in this
// package. It matches the Metric interface of the stable prometheus package,
// so concrete metrics can also be registered directly with a
// prometheus.Registry.
type Metric interface {
	// Desc returns the immutable descriptor of the metric.
	Desc() *Desc

	// Write encodes the metric into the provided Protocol Buffer object.
	//
	// Implementations must be safe for concurrent reads and must avoid
	// blocking, as scrapes may happen at any time.
	Write(*dto.Metric) error
}

// CollectorMetric is a [Metric] that a [Collector] can send during
// collection. It is an alias kept separate from Metric to mirror the naming
// used by the stable prometheus package.
type CollectorMetric = Metric

// Collector collects metrics for a scrape.
//
// The interface mirrors the Collector interface of the stable prometheus
// package. A [Group] implements it.
type Collector interface {
	// Describe sends every possible metric descriptor to ch.
	Describe(chan<- *Desc)

	// Collect sends the metrics belonging to one scrape to ch.
	Collect(chan<- CollectorMetric)
}

// Gatherer produces a self-consistent Protocol Buffer representation of the
// group's counters.
//
// Instances are safe for concurrent use. Every returned metric family is
// built from one [Group.Snapshot], so cross-counter invariants hold within
// the returned data.
type Gatherer struct {
	group *Group
}

// NewGatherer wraps group into a Protocol Buffer gatherer.
func NewGatherer(group *Group) *Gatherer {
	return &Gatherer{group: group}
}

// Gather returns the counters of the group as metric families.
//
// Counters sharing a fully-qualified metric name are grouped into one family.
// Families are sorted by name. Gather never returns an error for counters
// created by this package; the error result is kept to match the gatherer
// contract used across the Prometheus ecosystem.
func (g *Gatherer) Gather() ([]*dto.MetricFamily, error) {
	snapshot := g.group.Snapshot()

	families := make(map[string]*dto.MetricFamily)
	order := make([]string, 0)
	for _, sample := range snapshot.metrics {
		name := sample.Counter.Desc().fqName
		family, ok := families[name]
		if !ok {
			family = &dto.MetricFamily{
				Name: stringPtr(name),
				Help: stringPtr(sample.Counter.Desc().help),
				Type: dto.MetricType_COUNTER.Enum(),
			}
			families[name] = family
			order = append(order, name)
		}

		metric := &dto.Metric{Counter: &dto.Counter{Value: ptr(sample.Value)}}
		metric.Label = append(metric.Label, sample.Counter.labelPairs...)
		family.Metric = append(family.Metric, metric)
	}

	sort.Strings(order)
	result := make([]*dto.MetricFamily, 0, len(order))
	for _, name := range order {
		result = append(result, families[name])
	}
	return result, nil
}
