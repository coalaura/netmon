package main

import (
	"context"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

const icmpTargetCount = 3

type icmpTargetResult struct {
	Target   string
	Status   string
	Duration time.Duration
	Detail   string
}

var ICMPRemoteTargets = [icmpTargetCount]string{
	"9.9.9.9",
	"1.1.1.1",
	"8.8.8.8",
}

var icmpTargets = strings.Join(ICMPRemoteTargets[:], ";")

func pingRemoteTargets(ctx context.Context, state *interfaceState) probeResult {
	result := unavailableICMP("No usable IPv4 interface")

	var workers sync.WaitGroup

	for index, target := range ICMPRemoteTargets {
		workers.Go(func() {
			result.ICMP[index] = pingTarget(ctx, state, netip.MustParseAddr(target))
		})
	}

	workers.Wait()

	summarizeICMP(&result)

	return result
}

func pingTarget(ctx context.Context, state *interfaceState, target netip.Addr) icmpTargetResult {
	result := icmpTargetResult{Target: addressText(target), Status: statusUnavailable}

	if !target.IsValid() {
		result.Detail = "No gateway on this route"

		return result
	}

	result = runPing(ctx, state, target, runtime.GOOS == "windows")
	if runtime.GOOS == "linux" && result.Status == statusPermission {
		// Try raw ICMP when the host disallows ping sockets but grants CAP_NET_RAW.
		result = runPing(ctx, state, target, true)
	}

	return result
}

func runPing(ctx context.Context, state *interfaceState, target netip.Addr, privileged bool) icmpTargetResult {
	result := icmpTargetResult{Target: target.String(), Status: statusTimeout, Detail: "No echo reply within 800ms"}

	pinger := probing.New(target.String())
	pinger.SetNetwork("ip4")
	pinger.SetPrivileged(privileged)
	pinger.SetLogger(probing.NoopLogger{})
	pinger.Count = 1
	pinger.Timeout = probeTimeout
	pinger.ResolveTimeout = probeTimeout
	pinger.Source = state.Address.String()

	// Windows lacks IP_PKTINFO support in x/net; binding Source still selects the local address.
	if runtime.GOOS == "linux" {
		pinger.InterfaceName = state.Name
	}

	pinger.RecordRtts = false
	pinger.RecordTTLs = false

	pinger.OnRecv = func(packet *probing.Packet) {
		result.Status = statusOK
		result.Duration = packet.Rtt
		result.Detail = "Echo reply"

		if packet.TTL > 0 {
			result.Detail = "Echo reply; TTL " + strconv.Itoa(packet.TTL)
		}
	}

	err := pinger.RunWithContext(ctx)
	if err != nil {
		result.Status = errorStatus(err)
		result.Detail = err.Error()
	}

	return result
}

func unavailableICMP(detail string) probeResult {
	result := probeResult{Target: icmpTargets, Status: statusUnavailable, Detail: detail}

	for index, target := range ICMPRemoteTargets {
		result.ICMP[index] = icmpTargetResult{Target: target, Status: statusUnavailable, Detail: detail}
	}

	return result
}

func summarizeICMP(result *probeResult) {
	result.Status = statusUnavailable
	result.Duration = 0
	result.ICMPAttempted = 0
	result.ICMPSucceeded = 0

	var detail strings.Builder

	detail.Grow(128)

	for index := range result.ICMP {
		target := &result.ICMP[index]

		if index > 0 {
			detail.WriteString(" | ")
		}

		detail.WriteString(target.Target)
		detail.WriteByte(' ')

		attempted, succeeded := outcomeCounts(target.Status)

		result.ICMPAttempted += attempted
		result.ICMPSucceeded += succeeded

		if target.Status == statusOK {
			result.Duration += target.Duration

			detail.WriteString(strconv.FormatFloat(float64(target.Duration)/float64(time.Millisecond), 'f', 1, 64))
			detail.WriteString("ms")
		} else {
			detail.WriteString(target.Status)
		}

		if statusSeverity(target.Status) > statusSeverity(result.Status) {
			result.Status = target.Status
		}
	}

	if result.ICMPSucceeded > 0 {
		result.Duration /= time.Duration(result.ICMPSucceeded)
		result.Status = statusPartial

		if result.ICMPSucceeded == icmpTargetCount {
			result.Status = statusOK
		}
	}

	result.Detail = detail.String()
}
