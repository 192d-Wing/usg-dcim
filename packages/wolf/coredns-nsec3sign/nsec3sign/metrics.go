// Prometheus metrics.
//
// We mirror the upstream `dnssec` plugin's three core metrics — same
// `Subsystem` (we substitute `nsec3sign`), same metric names, same
// labels — so dashboards built for `coredns_dnssec_*` translate to
// `coredns_nsec3sign_*` with only a metric-name rewrite. Operators
// running both plugins side-by-side (during a NSEC→NSEC3 migration)
// can compare apples to apples.
//
// One nsec3sign-specific gauge: `chain_entries{zone}` reports the
// node count of each loaded chain. Useful for spotting a zone-file
// reload that unexpectedly shrunk the chain.

package nsec3sign

import (
	"github.com/coredns/coredns/plugin"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// cacheEntries reports the live cache occupancy. type="signature"
	// matches the upstream `dnssec` plugin's labeling so a unified
	// dashboard doesn't need a separate selector for our subsystem.
	cacheEntries = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: plugin.Namespace,
		Subsystem: "nsec3sign",
		Name:      "cache_entries",
		Help:      "The number of elements in the nsec3sign signature cache.",
	}, []string{"server", "type"})

	cacheHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: plugin.Namespace,
		Subsystem: "nsec3sign",
		Name:      "cache_hits_total",
		Help:      "The count of nsec3sign signature-cache hits.",
	}, []string{"server"})

	cacheMisses = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: plugin.Namespace,
		Subsystem: "nsec3sign",
		Name:      "cache_misses_total",
		Help:      "The count of nsec3sign signature-cache misses.",
	}, []string{"server"})

	// denialsIssued counts the denial proofs we built — useful for
	// spotting a zone that's getting hammered with NXDOMAINs (cache
	// poisoning attempt, mis-configured client). Split by `type`:
	// `nxdomain` for closest-encloser proofs, `nodata` for
	// matching NSEC3 proofs.
	denialsIssued = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: plugin.Namespace,
		Subsystem: "nsec3sign",
		Name:      "denials_total",
		Help:      "The number of denial-of-existence proofs constructed, by response type.",
	}, []string{"server", "type"})

	// chainEntries reports the chain size for each loaded zone. Set
	// once at startup (and again on each reload); useful for
	// catching a zone-file reload that unexpectedly shrunk the
	// chain — e.g. a renderer bug that dropped half the hosts.
	chainEntries = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: plugin.Namespace,
		Subsystem: "nsec3sign",
		Name:      "chain_entries",
		Help:      "The number of owner names in each loaded NSEC3 chain.",
	}, []string{"zone"})
)
