// Copyright 2022 The Prometheus Authors
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

// A simple example of how to record a latency metric with exemplars, using a fictional id
// as an exemplar label. A request-scoped value such as a trace ID must never be
// attached as a regular metric label because it would create one time series per
// request and unbounded memory growth. Exemplars are kept in bounded, per-metric
// storage instead, so the trace ID stays available for correlation without adding
// time series or growing the scrape payload.

package main

import (
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	requestDurations := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "A histogram of the HTTP request durations in seconds.",
		Unit:    "seconds",
		Buckets: prometheus.ExponentialBuckets(0.1, 1.5, 5),
	})

	// Create non-global registry.
	registry := prometheus.NewRegistry()

	// Add go runtime metrics and process collectors.
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		requestDurations,
	)

	go func() {
		for {
			// Record fictional latency.
			now := time.Now()
			// The dummy ID replaces the trace ID used to correlate this
			// observation with a distributed trace. It is passed as an
			// exemplar label, not as a metric label, to keep the number of
			// time series constant regardless of request cardinality.
			requestDurations.(prometheus.ExemplarObserver).ObserveWithExemplar(
				time.Since(now).Seconds(), prometheus.Labels{"dummyID": strconv.Itoa(rand.Intn(100000))},
			)
			time.Sleep(600 * time.Millisecond)
		}
	}()

	// Expose /metrics HTTP endpoint using the created custom registry.
	http.Handle(
		"/metrics", promhttp.HandlerFor(
			registry,
			promhttp.HandlerOpts{
				EnableOpenMetrics: true,
			},
		),
	)
	// To test: curl -H 'Accept: application/openmetrics-text' localhost:8080/metrics
	log.Fatalln(http.ListenAndServe(":8080", nil))
}
