package promx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

func TestMountExposesMetrics(t *testing.T) {
	mux := http.NewServeMux()
	Mount(mux)

	// Register a counter so the exposition isn't empty.
	c := promauto.With(prometheus.DefaultRegisterer).NewCounter(prometheus.CounterOpts{
		Name: "test_promx_counter_total",
		Help: "Test counter used by TestMountExposesMetrics.",
	})
	c.Inc()
	t.Cleanup(func() { prometheus.DefaultRegisterer.Unregister(c) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "test_promx_counter_total 1") {
		t.Fatalf("counter not exposed; body:\n%s", rec.Body.String())
	}
}
