# client_golang experimental module

Contains experimental utilities and APIs for Prometheus.

Packages within this module are listed below.

## Remote

This module contains production quality code with explicitly unstable API. Any code in this module can change its API or be removed; use with care.

The intention is that, with maturity some of the packages would graduate to stable version of client_golang module.

Packages within this module are listed below.

## Remote

NOTE: The `api/remote` package is used by `prometheus/prometheus`. Any changes to this package should ensure Prometheus is not affected (e.g. does not break Prometheus on upgrade or if it breaks, the Prometheus gets updated soon.
Implements bindings from Prometheus remote APIs (remote write v1 and v2 for now).

Contains flexible method for building API clients, that can send remote write protocol messages.

```go
    import (
        "github.com/prometheus/client_golang/exp/api/remote"
    )
    ...

	remoteAPI, err := remote.NewAPI(
		"https://your-remote-endpoint",
		remote.WithAPIHTTPClient(httpClient),
		remote.WithAPILogger(logger.With("component", "remote_write_api")),
	)
    ...

    stats, err := remoteAPI.Write(ctx, remote.WriteV2MessageType, protoWriteReq)
```

Also contains handler methods for applications that would like to handle and store remote write requests.

```go
    import (
        "net/http"
        "log"

        "github.com/prometheus/client_golang/exp/api/remote"
    )
    ...
    
    type db {}

    func NewStorage() *db {}

    func (d *db) Store(ctx context.Context, msgType remote.WriteMessageType, req *http.Request) (*remote.WriteResponse, error) {}
    ...

	mux := http.NewServeMux()

	remoteWriteHandler := remote.NewHandler(storage, remote.WithHandlerLogger(logger.With("component", "remote_write_handler")))
	mux.Handle("/api/v1/write", remoteWriteHandler)

	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
```

For more details, see [go doc](https://pkg.go.dev/github.com/prometheus/client_golang/exp/api/remote).

## Cardinality

The `cardinality` package helps bound the cardinality of label values that are
derived from untrusted or unbounded input, such as HTTP request paths taken
from user input. It provides two tools whose method values satisfy the
`func(string) string` signature of `prometheus.LabelConstraint`, so they plug
directly into `ConstrainedLabels` without changing the instrumentation call
site:

- `Normalizer` rewrites request paths into stable route templates by
  collapsing identifier-like segments (numeric IDs, UUIDs, long hashes) into a
  shared sentinel, preserving route-level discrimination while removing the
  per-resource dimension.
- `Limiter` retains the first `MaxCardinality` distinct values verbatim and
  collapses every further distinct value into a shared overflow value, hard
  capping the number of exported series.

```go
    import (
        "github.com/prometheus/client_golang/exp/cardinality"
    )
    ...

	normalizer := cardinality.NewNormalizer(cardinality.NormalizerOpts{})
	limiter, err := cardinality.NewLimiter(cardinality.LimiterOpts{
		MaxCardinality: 100,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Normalize first to keep route discrimination, then cap whatever remains.
	pathConstraint := func(path string) string {
		return limiter.Limit(normalizer.Normalize(path))
	}

	requests := prometheus.V2.NewCounterVec(prometheus.CounterVecOpts{
		CounterOpts: prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests.",
		},
		VariableLabels: prometheus.ConstrainedLabels{
			{Name: "path", Constraint: pathConstraint},
		},
	})
```

For more details, see the package documentation of
`github.com/prometheus/client_golang/exp/cardinality`.
