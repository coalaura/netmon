package main

import (
	"slices"
	"time"
)

type latencySummary struct {
	Median      time.Duration
	P95         time.Duration
	Jitter      time.Duration
	Samples     int
	JitterPairs int
	Attempts    int
	Failed      int
}

func (stats *latencySummary) finish(values []time.Duration) {
	if stats.Samples == 0 {
		return
	}

	ordered := values[:stats.Samples]
	slices.Sort(ordered)

	stats.Median = ordered[stats.Samples/2]

	if stats.Samples%2 == 0 {
		stats.Median = (ordered[stats.Samples/2-1] + stats.Median) / 2
	}

	// Nearest-rank percentile; jitter is mean absolute change between consecutive replies.
	stats.P95 = ordered[(95*stats.Samples+99)/100-1]

	if stats.JitterPairs > 0 {
		stats.Jitter /= time.Duration(stats.JitterPairs)
	}
}

// Called with monitor.mu held. Scratch storage is reused for each individual target.
func (dashboard *dashboard) refreshStats() {
	monitor := dashboard.monitor

	var values [historySize]time.Duration

	for probeIndex := range probeCount {
		for hostIndex := range 3 {
			stats := &dashboard.stats[probeIndex][hostIndex]
			*stats = latencySummary{}

			target, _, _ := hostOutcome(&dashboard.current.Probes[probeIndex], hostIndex)
			if target == "" {
				continue
			}

			var (
				previousTime     time.Time
				previousDuration time.Duration
			)

			for offset := range monitor.count {
				index := (monitor.next - monitor.count + offset + historySize) % historySize

				current := &monitor.history[index]
				result := &current.Probes[probeIndex]

				host, status, duration := hostOutcome(result, hostIndex)

				sameInterface := current.Interface.Index == dashboard.current.Interface.Index && current.Interface.Address == dashboard.current.Interface.Address

				if current.Time.Before(dashboard.start) || !sameInterface || result.Target != dashboard.current.Probes[probeIndex].Target || host != target {
					previousTime = time.Time{}

					continue
				}

				attempts, successes := outcomeCounts(status)

				stats.Attempts += attempts
				stats.Failed += attempts - successes

				if status != statusOK {
					previousTime = time.Time{}

					continue
				}

				values[stats.Samples] = duration

				stats.Samples++

				gap := current.Time.Sub(previousTime)
				if !previousTime.IsZero() && gap > 0 && gap <= 1500*time.Millisecond {
					stats.Jitter += (duration - previousDuration).Abs()
					stats.JitterPairs++
				}

				previousTime = current.Time
				previousDuration = duration
			}

			stats.finish(values[:])
		}
	}
}

func hostOutcome(result *probeResult, index int) (string, string, time.Duration) {
	if result.ICMP[0].Target != "" {
		host := &result.ICMP[index]
		return host.Target, host.Status, host.Duration
	}

	if result.Query != "" {
		host := &result.DNS[index]
		return host.Query, host.Status, host.Duration
	}

	if index == 0 {
		return result.Target, result.Status, result.Duration
	}

	return "", "", 0
}
