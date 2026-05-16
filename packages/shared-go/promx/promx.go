// Package promx is a thin wrapper that mounts /metrics on a mux using
// the default Prometheus registry. Each animal service registers its
// own counters/histograms with `prometheus.MustRegister`; this package
// just owns the HTTP plumbing so it's consistent across services.
package promx

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Mount registers the Prometheus exposition handler at /metrics on
// the given mux. Uses the default registry so callers can keep using
// promauto / prometheus.MustRegister with no extra wiring.
func Mount(mux *http.ServeMux) {
	mux.Handle("/metrics", promhttp.Handler())
}
