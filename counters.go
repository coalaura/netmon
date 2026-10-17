package main

import "time"

type interfaceCounters struct {
	Time       time.Time
	Identity   uint64
	RXBytes    uint64
	TXBytes    uint64
	RXErrors   uint64
	TXErrors   uint64
	RXDiscards uint64
	TXDiscards uint64
	Available  bool
	Detail     string
}

type trafficSample struct {
	Counters        interfaceCounters
	Interval        time.Duration
	RXBitsPerSecond float64
	TXBitsPerSecond float64
	RXErrors        uint64
	TXErrors        uint64
	RXDiscards      uint64
	TXDiscards      uint64
	Valid           bool
	Detail          string
}

func measureTraffic(state, previous *interfaceState, counters, before *interfaceCounters) trafficSample {
	result := trafficSample{Counters: *counters, Detail: counters.Detail}

	if !counters.Available {
		return result
	}

	if !before.Available || before.Identity != counters.Identity || previous.Index != state.Index || previous.Name != state.Name || previous.Link != state.Link {
		result.Detail = "Waiting for counter baseline"

		return result
	}

	result.Interval = counters.Time.Sub(before.Time)
	reset := counters.RXBytes < before.RXBytes || counters.TXBytes < before.TXBytes || counters.RXErrors < before.RXErrors || counters.TXErrors < before.TXErrors || counters.RXDiscards < before.RXDiscards || counters.TXDiscards < before.TXDiscards

	if reset || result.Interval <= 0 {
		result.Detail = "Counter reset; collecting a new baseline"

		return result
	}

	result.Valid = true
	result.RXBitsPerSecond = float64(counters.RXBytes-before.RXBytes) * 8 / result.Interval.Seconds()
	result.TXBitsPerSecond = float64(counters.TXBytes-before.TXBytes) * 8 / result.Interval.Seconds()
	result.RXErrors = counters.RXErrors - before.RXErrors
	result.TXErrors = counters.TXErrors - before.TXErrors
	result.RXDiscards = counters.RXDiscards - before.RXDiscards
	result.TXDiscards = counters.TXDiscards - before.TXDiscards
	result.Detail = "Passive interface counters"

	return result
}
