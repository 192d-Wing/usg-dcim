package dnsagent

import (
	"sort"
	"strconv"
	"strings"
)

// promSnapshot is the rolled-up shape we hand to the metrics POST.
// Mirrors collector/dns_agent.py::_parse_prom_text — engine-specific
// series fold into a uniform counters dict the central API understands.
type promSnapshot struct {
	RequestsTotal   int64
	NoError         int64
	NXDomain        int64
	ServFail        int64
	DurationBuckets map[float64]int64 // le_seconds → cumulative count
	DurationCount   int64
}

func newPromSnapshot() *promSnapshot {
	return &promSnapshot{DurationBuckets: map[float64]int64{}}
}

// parsePromText reads Prometheus text-format output and folds known
// CoreDNS + Hickory series into a promSnapshot. Multi-line series
// (different label combos) are summed; unknown series are dropped.
func parsePromText(text string) *promSnapshot {
	c := newPromSnapshot()
	for _, raw := range strings.Split(text, "\n") {
		name, labels, value, ok := parsePromLine(raw)
		if !ok {
			continue
		}
		absorb(c, name, labels, value)
	}
	return c
}

func absorb(c *promSnapshot, name string, labels map[string]string, value float64) {
	switch {
	case strings.HasPrefix(name, "coredns_"):
		absorbCoreDNS(c, name, labels, value)
	case strings.HasPrefix(name, "hickory_"):
		absorbHickory(c, name, labels, value)
	}
}

var rcodeKey = map[string]string{
	"NOERROR": "noerror", "NXDOMAIN": "nxdomain", "SERVFAIL": "servfail",
}

func absorbCoreDNS(c *promSnapshot, name string, labels map[string]string, v float64) {
	switch name {
	case "coredns_dns_requests_total":
		c.RequestsTotal += int64(v)
	case "coredns_dns_responses_total":
		switch rcodeKey[strings.ToUpper(labels["rcode"])] {
		case "noerror":
			c.NoError += int64(v)
		case "nxdomain":
			c.NXDomain += int64(v)
		case "servfail":
			c.ServFail += int64(v)
		}
	case "coredns_dns_request_duration_seconds_bucket":
		absorbBucket(c, labels, v)
	case "coredns_dns_request_duration_seconds_count":
		c.DurationCount += int64(v)
	}
}

func absorbHickory(c *promSnapshot, name string, labels map[string]string, v float64) {
	switch name {
	case "hickory_request_record_types_total":
		c.RequestsTotal += int64(v)
	case "hickory_resolver_cache_miss_duration_seconds_bucket":
		absorbBucket(c, labels, v)
	case "hickory_resolver_cache_miss_duration_seconds_count":
		c.DurationCount += int64(v)
	}
}

func absorbBucket(c *promSnapshot, labels map[string]string, v float64) {
	le, err := strconv.ParseFloat(labels["le"], 64)
	if err != nil {
		// "+Inf" parses fine; an unrecognised value is logged-skip-worthy
		// only at debug — callers won't see it under normal load.
		return
	}
	c.DurationBuckets[le] += int64(v)
}

// parsePromLine: name{label="v",...} value
func parsePromLine(line string) (string, map[string]string, float64, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", nil, 0, false
	}
	idx := strings.LastIndex(line, " ")
	if idx < 0 {
		return "", nil, 0, false
	}
	metricPart := strings.TrimSpace(line[:idx])
	valueStr := strings.TrimSpace(line[idx+1:])
	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return "", nil, 0, false
	}
	name, labelBlob, _ := strings.Cut(metricPart, "{")
	labels := map[string]string{}
	if labelBlob != "" {
		labelBlob = strings.TrimSuffix(labelBlob, "}")
		for _, item := range strings.Split(labelBlob, ",") {
			k, v, ok := strings.Cut(item, "=")
			if !ok {
				continue
			}
			labels[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return name, labels, value, true
}

// percentileFromBuckets linearly interpolates the requested percentile
// across cumulative Prom histogram buckets. Returns 0/false when the
// total is too low (<5) or when the target falls in the +Inf bucket.
// Output is milliseconds (buckets are in seconds).
func percentileFromBuckets(buckets map[float64]int64, total int64, p float64) (float64, bool) {
	if total < 5 || len(buckets) == 0 {
		return 0, false
	}
	type entry struct {
		le    float64
		count int64
	}
	all := make([]entry, 0, len(buckets))
	for le, c := range buckets {
		all = append(all, entry{le, c})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].le < all[j].le })
	target := float64(total) * p
	var prevLE float64
	var prevCount float64
	for _, e := range all {
		if float64(e.count) >= target {
			if isInfPlus(e.le) {
				return 0, false
			}
			span := float64(e.count) - prevCount
			if span <= 0 {
				return e.le * 1000, true
			}
			frac := (target - prevCount) / span
			return (prevLE + frac*(e.le-prevLE)) * 1000, true
		}
		prevLE = e.le
		prevCount = float64(e.count)
	}
	return 0, false
}

func isInfPlus(f float64) bool {
	// Prom emits +Inf which strconv.ParseFloat parses as math.Inf(1).
	// Compare by ratio so we don't pull in math just for IsInf.
	return f > 1e308
}
