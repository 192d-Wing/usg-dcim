// Package ipamutilization is the Go port of Python's
// ipam_utilization_sweep cron in packages/otter/src/dcim/worker.py:195.
// Every 5 minutes it walks every Subnet + Supernet and emits the
// `dcim_ipam_subnet_free_percent{fabric_id,subnet_id}` +
// `dcim_ipam_supernet_free_percent{fabric_id,supernet_id}` gauges
// that the Prometheus scrape picks up off the otter-go-scheduler
// /metrics endpoint.
//
// Wire-shape parity with Python:
//   - subnet capacity = num_addresses - 2 (network + broadcast)
//     except for /32 + /31 IPv4 and /127 + /128 IPv6 — those clamp
//     to 0 capacity → free_percent = 100. Matches
//     services.ipam_metrics._prefix_capacity.
//   - supernet capacity = num_addresses (no -2 deduct) since the
//     calculation is "how much of the address space is carved into
//     subnets", not allocatable-host count.
//   - carved_capacity for a supernet = sum of child subnet
//     capacities (each computed with the -2 deduct). Walking the
//     subnets first and folding into a per-supernet map matches
//     Python's two-pass shape so the gauges agree on every tick.
package ipamutilization

import (
	"context"
	"errors"
	"math/big"
	"net"
	"strings"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

const Name = "ipam_utilization_sweep"

// Querier is the slim sqlc surface the job calls. *dbq.Queries
// satisfies it; tests inject a fake that returns canned rows.
type Querier interface {
	ListSubnetsForUtilization(ctx context.Context) ([]dbq.SubnetForUtilizationRow, error)
	ListSupernetsForUtilization(ctx context.Context) ([]dbq.SupernetForUtilizationRow, error)
	ListActiveReservedAddressCountsBySubnet(ctx context.Context) ([]dbq.ActiveReservedAddressCountRow, error)
}

// Gauges holds the two Prometheus Gauge vectors the job sets. Wired
// from cmd/otter-go-scheduler/main.go so the metric registration
// happens in one place (avoids double-registration when the package
// is imported from tests). Field names mirror Python's gauge names.
type Gauges struct {
	SubnetFreePercent   *prometheus.GaugeVec
	SupernetFreePercent *prometheus.GaugeVec
}

// NewGauges registers the two gauges on reg and returns the struct
// the Job uses. Tests pass a fresh prometheus.NewRegistry so
// independent test runs don't collide on the global default
// registry.
func NewGauges(reg prometheus.Registerer) *Gauges {
	g := &Gauges{
		SubnetFreePercent: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dcim_ipam_subnet_free_percent",
			Help: "Per-subnet percentage of allocatable addresses still free.",
		}, []string{"fabric_id", "subnet_id"}),
		SupernetFreePercent: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dcim_ipam_supernet_free_percent",
			Help: "Per-supernet percentage of address space not yet carved into subnets.",
		}, []string{"fabric_id", "supernet_id"}),
	}
	reg.MustRegister(g.SubnetFreePercent, g.SupernetFreePercent)
	return g
}

type Job struct {
	Q      Querier
	Gauges *Gauges
}

func (j *Job) Name() string { return Name }

func (j *Job) Run(ctx context.Context) (map[string]any, error) {
	if j.Q == nil {
		return nil, errors.New("ipamutilization: Querier is nil")
	}
	if j.Gauges == nil {
		return nil, errors.New("ipamutilization: Gauges is nil")
	}
	subnets, err := j.Q.ListSubnetsForUtilization(ctx)
	if err != nil {
		return nil, err
	}
	countRows, err := j.Q.ListActiveReservedAddressCountsBySubnet(ctx)
	if err != nil {
		return nil, err
	}
	usedBySubnet := make(map[uuid.UUID]int64, len(countRows))
	for _, r := range countRows {
		usedBySubnet[r.SubnetID] = r.UsedCount
	}
	carvedBySupernet := make(map[uuid.UUID]int64)
	var subnetsEmitted int
	for _, s := range subnets {
		cap := subnetCapacity(s.Prefix)
		used := usedBySubnet[s.ID]
		if used > cap {
			used = cap // mirror Python's `min(used, cap)` clamp
		}
		freePct := freePercent(cap, used)
		j.Gauges.SubnetFreePercent.WithLabelValues(s.FabricID.String(), s.ID.String()).Set(freePct)
		subnetsEmitted++
		if s.SupernetID != nil {
			carvedBySupernet[*s.SupernetID] += cap
		}
	}
	supernets, err := j.Q.ListSupernetsForUtilization(ctx)
	if err != nil {
		return nil, err
	}
	var supernetsEmitted int
	for _, sn := range supernets {
		cap := supernetCapacity(sn.Prefix)
		carved := carvedBySupernet[sn.ID]
		if carved > cap {
			carved = cap
		}
		freePct := freePercent(cap, carved)
		j.Gauges.SupernetFreePercent.WithLabelValues(sn.FabricID.String(), sn.ID.String()).Set(freePct)
		supernetsEmitted++
	}
	return map[string]any{
		"subnets_emitted":   subnetsEmitted,
		"supernets_emitted": supernetsEmitted,
	}, nil
}

// subnetCapacity returns the allocatable-host count Python's
// services.ipam_metrics._prefix_capacity produces. For "normal"
// prefixes that's num_addresses - 2 (the network + broadcast
// addresses are not allocatable to hosts). Tiny prefixes
// (/31 + /32 IPv4, /127 + /128 IPv6) clamp to 0 — a /32
// point-to-point with no consumers reads as 100% free because
// there's nothing to allocate.
func subnetCapacity(prefix string) int64 {
	n := numAddresses(prefix)
	if n.Sign() <= 0 {
		return 0
	}
	cap := new(big.Int).Sub(n, big.NewInt(2))
	if cap.Sign() < 0 {
		return 0
	}
	return clampInt64(cap)
}

// supernetCapacity uses raw num_addresses (no -2 deduct) — the
// question for supernets is "how much of the address SPACE is carved
// into subnets" not "how many client addresses are allocatable".
func supernetCapacity(prefix string) int64 {
	n := numAddresses(prefix)
	return clampInt64(n)
}

// numAddresses parses prefix as CIDR and returns the total number of
// addresses in the block. Returns 0 on parse failure — matches
// Python's `except (TypeError, ValueError): cap = 0` branch.
func numAddresses(prefix string) *big.Int {
	prefix = strings.TrimSpace(prefix)
	_, ipnet, err := net.ParseCIDR(prefix)
	if err != nil {
		return big.NewInt(0)
	}
	ones, bits := ipnet.Mask.Size()
	if bits == 0 {
		return big.NewInt(0)
	}
	return new(big.Int).Lsh(big.NewInt(1), uint(bits-ones))
}

// clampInt64 caps a big.Int at MaxInt64. The Prometheus gauge is a
// float64 so very large supernets (e.g. an IPv6 /16 with 2^112
// addresses) round-trip through float anyway; clamping the integer
// path keeps the carved% comparison sensible.
func clampInt64(n *big.Int) int64 {
	maxInt64 := big.NewInt(int64(^uint64(0) >> 1))
	if n.Cmp(maxInt64) > 0 {
		return maxInt64.Int64()
	}
	return n.Int64()
}

// freePercent returns the percent-free Python's compute_*_utilization
// helpers emit. cap == 0 → 100.0 (nothing to use → 100% free).
// Otherwise 100 * (cap - used) / cap.
func freePercent(cap, used int64) float64 {
	if cap <= 0 {
		return 100.0
	}
	return 100.0 * float64(cap-used) / float64(cap)
}
