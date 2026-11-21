module github.com/prometheus/client_golang/exp

go 1.26.0

require (
	github.com/google/go-cmp v0.7.0
	github.com/klauspost/compress v1.20.1
	github.com/prometheus/client_golang v1.23.2
	github.com/prometheus/common v0.73.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/hashicorp/go-uuid v1.0.3 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pandatix/go-abnf v0.5.1 // indirect
	github.com/prometheus/client_model v0.6.3
	github.com/prometheus/procfs v0.22.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/prometheus/client_golang => ../
