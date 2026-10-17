package main

import (
	"context"
	"fmt"
	"net/netip"
	"time"
)

const (
	traceInterval    = 20 * time.Second
	traceMaxHops     = 30
	traceWindow      = time.Hour
	traceHistorySize = int(traceWindow / traceInterval)
	statusIncomplete = "incomplete"
	statusCancelled  = "cancelled"
)

type traceHop struct {
	Time     time.Time
	TTL      int
	Address  netip.Addr
	Status   string
	Duration time.Duration
	Detail   string
	Reached  bool
	Terminal bool
}

type traceResult struct {
	Started   time.Time
	Finished  time.Time
	Interface interfaceState
	Target    netip.Addr
	Protocol  string
	Status    string
	Detail    string
	Change    string
	HopCount  int
	Count     int
	Hops      [traceMaxHops]traceHop
}

type traceSampler struct {
	results     chan *traceResult
	cancel      context.CancelFunc
	state       interfaceState
	next        time.Time
	invalidated bool
}

func (sampler *traceSampler) advance(ctx context.Context, state interfaceState, now time.Time) *traceResult {
	changed := !sameTraceNetwork(&sampler.state, &state)
	if changed {
		sampler.next = time.Time{}
		if sampler.cancel != nil {
			sampler.invalidated = true
			sampler.cancel()
		}
	}

	var completed *traceResult

	if sampler.cancel != nil {
		select {
		case completed = <-sampler.results:
			sampler.cancel()

			sampler.cancel = nil
			if sampler.invalidated {
				completed.Status = statusCancelled
				completed.HopCount = 0
				completed.Detail = "Interface or route changed during trace"
			}
		default:
		}
	}

	overran := completed != nil && !completed.Finished.Before(sampler.next)
	if !changed && !sampler.next.IsZero() && !now.Before(sampler.next) && (sampler.cancel != nil || overran) {
		// Skip occupied slots instead of starting a catch-up trace after an overrun.
		skipped := int64(now.Sub(sampler.next)/traceInterval) + 1
		sampler.next = sampler.next.Add(time.Duration(skipped) * traceInterval)
	}

	if sampler.cancel == nil && !now.Before(sampler.next) && state.Link == "up" && state.Address.Is4() && state.DefaultRoute && ctx.Err() == nil {
		if sampler.results == nil {
			sampler.results = make(chan *traceResult, 1)
		}

		traceContext, cancel := context.WithCancel(ctx)

		sampler.cancel = cancel
		sampler.invalidated = false
		sampler.state = state
		sampler.next = now.Add(traceInterval)

		go func() {
			sampler.results <- runTrace(traceContext, state, netip.MustParseAddr(ICMPRemoteTargets[0]))
		}()
	}

	return completed
}

func (sampler *traceSampler) stop() *traceResult {
	if sampler.cancel == nil {
		return nil
	}

	sampler.cancel()

	result := <-sampler.results

	sampler.cancel = nil

	if sampler.invalidated {
		result.Status = statusCancelled
		result.HopCount = 0
		result.Detail = "Interface or route changed during trace"
	}

	return result
}

func (monitor *monitor) recordTrace(result *traceResult) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()

	var last *traceResult

	if monitor.traceCount > 0 {
		last = monitor.traces[(monitor.traceNext-1+traceHistorySize)%traceHistorySize]
	}

	if result.Status == statusOK {
		for offset := range monitor.traceCount {
			index := (monitor.traceNext - 1 - offset + traceHistorySize) % traceHistorySize

			previous := monitor.traces[index]
			if !sameTraceNetwork(&result.Interface, &previous.Interface) {
				break
			}

			if previous.Status == statusOK {
				result.Change = traceChange(previous, result)

				break
			}
		}
	}

	monitor.traces[monitor.traceNext] = result

	if traceStateChanged(last, result) {
		message := "Traceroute: " + result.Status + " | " + result.Detail

		if result.Change != "" {
			message += " | " + result.Change
		}

		monitor.addEvent(result.Finished, message)
	}

	monitor.traceNext = (monitor.traceNext + 1) % traceHistorySize
	monitor.traceCount = min(monitor.traceCount+1, traceHistorySize)
}

func runTrace(ctx context.Context, state interfaceState, target netip.Addr) *traceResult {
	result := &traceResult{Started: time.Now(), Interface: state, Target: target, Protocol: traceProtocol, Status: statusIncomplete}

	probe, err := openTrace(&state, target)
	if err != nil {
		result.Status = errorStatus(err)
		result.Detail = err.Error()
		result.Finished = time.Now()

		return result
	}

	defer probe.close()

	for ttl := 1; ttl <= traceMaxHops && ctx.Err() == nil; ttl++ {
		started := time.Now()

		hop := probe.hop(ctx, ttl)
		hop.Time = started

		result.Hops[result.Count] = hop
		result.Count++

		if hop.Reached && hop.Address == target {
			result.Status = statusOK
			result.HopCount = ttl
			result.Detail = "Destination reached"

			break
		}

		if hop.Terminal {
			result.Status = hop.Status
			result.Detail = hop.Detail

			if result.Status == statusOK {
				result.Status = statusIncomplete
				result.Detail = "Terminal reply did not come from the destination"
			}

			break
		}
	}

	if ctx.Err() != nil {
		result.Status = statusCancelled
		result.HopCount = 0
		result.Detail = "Trace cancelled"
	} else if result.Detail == "" {
		result.Detail = "Destination not reached within 30 hops; missing replies may be filtered"
	}

	result.Finished = time.Now()

	return result
}

func sameTraceNetwork(first, second *interfaceState) bool {
	return first.Index == second.Index && first.Address == second.Address && first.Gateway == second.Gateway && first.Link == second.Link && first.DefaultRoute == second.DefaultRoute
}

func traceChange(previous, current *traceResult) string {
	if previous.Status != statusOK || current.Status != statusOK || previous.Target != current.Target || previous.Protocol != current.Protocol || !sameTraceNetwork(&previous.Interface, &current.Interface) {
		return ""
	}

	if previous.HopCount != current.HopCount {
		return fmt.Sprintf("Hop count changed: %d -> %d", previous.HopCount, current.HopCount)
	}

	for index := range min(previous.Count, current.Count) {
		before := previous.Hops[index].Address
		after := current.Hops[index].Address

		if before.IsValid() && after.IsValid() && before != after {
			return fmt.Sprintf("Observed path changed at hop %d: %s -> %s", index+1, before, after)
		}
	}

	return ""
}

func traceStateChanged(previous, current *traceResult) bool {
	if previous == nil || previous.Status != current.Status || previous.Target != current.Target || previous.Protocol != current.Protocol || !sameTraceNetwork(&previous.Interface, &current.Interface) {
		return true
	}

	if current.Status == statusOK {
		return current.Change != ""
	}

	return previous.Detail != current.Detail
}
