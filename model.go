package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"
)

const (
	sampleInterval    = time.Second
	probeTimeout      = 800 * time.Millisecond
	historySize       = 300
	probeCount        = 5
	statusOK          = "ok"
	statusPartial     = "partial"
	statusDNSError    = "dns_error"
	statusTimeout     = "timeout"
	statusError       = "error"
	statusUnavailable = "unavailable"
	statusPermission  = "permission"
)

type interfaceState struct {
	Index          int
	Name           string
	Address        netip.Addr
	Gateway        netip.Addr
	Resolver       netip.Addr
	ResolverSource string
	Link           string
	Speed          uint64
	ARP            string
	DefaultRoute   bool
	Error          string
}

type probeResult struct {
	Target        string
	Query         string
	Status        string
	Duration      time.Duration
	Detail        string
	DNS           [dnsTargetCount]dnsQueryResult
	DNSAttempted  int
	DNSSucceeded  int
	ICMP          [icmpTargetCount]icmpTargetResult
	ICMPAttempted int
	ICMPSucceeded int
}

type sample struct {
	Time        time.Time
	Interface   interfaceState
	Probes      [probeCount]probeResult
	Traffic     trafficSample
	Connections connectionSample
}

type monitor struct {
	mu                 sync.RWMutex
	history            [historySize]sample
	next               int
	count              int
	version            uint64
	logPath            string
	logError           string
	done               chan struct{}
	traces             [traceHistorySize]*traceResult
	traceNext          int
	traceCount         int
	traceRunning       bool
	traceDue           time.Time
	application        *applicationSample
	applications       [historySize]*applicationSample
	applicationNext    int
	applicationCount   int
	applicationRunning bool
	events             [32]string
	eventNext          int
	eventCount         int
}

var probeNames = [probeCount]string{"Gateway ICMP", "Internet ICMP", "Interface DNS", "Public DNS", "Gateway DNS"}

func (result *probeResult) counts() (int, int) {
	if result.ICMP[0].Target != "" {
		return result.ICMPAttempted, result.ICMPSucceeded
	}

	if result.Query != "" {
		return result.DNSAttempted, result.DNSSucceeded
	}

	return outcomeCounts(result.Status)
}

func (monitor *monitor) run(ctx context.Context, writer *csvLog, notify func()) {
	defer close(monitor.done)

	defer func() {
		if writer == nil {
			return
		}

		err := writer.close()
		if err != nil {
			log.Errorf("Closing CSV: %v", err)
		}
	}()

	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()

	var (
		tracer       traceSampler
		applications applicationSampler
		connections  connectionSampler
	)

	defer func() {
		result := applications.stop()
		if result != nil && writer != nil {
			err := writer.writeApplication(result)
			if err != nil {
				log.Errorf("Writing final application checks: %v", err)
			}
		}
	}()

	defer func() {
		result := tracer.stop()
		if result != nil && writer != nil {
			monitor.recordTrace(result)

			err := writer.writeTrace(result)
			if err != nil {
				log.Errorf("Writing final traceroute: %v", err)
			}
		}
	}()

	var (
		previous         interfaceState
		previousCounters interfaceCounters
		gateway          netip.Addr
		gatewayInterface int
		gatewayDNS       bool
		dnsWarmed        bool
		nextGatewayCheck time.Time
	)

	for ctx.Err() == nil {
		current := sample{Time: time.Now()}
		current.Interface = discoverInterface(ctx, previous)

		counters := readInterfaceCounters(&current.Interface)

		current.Traffic = measureTraffic(&current.Interface, &previous, &counters, &previousCounters)

		previousCounters = counters

		previous = current.Interface

		current.Connections = connections.sample()

		if !dnsWarmed && current.Interface.Address.IsValid() && current.Interface.Link == "up" {
			servers := [3]netip.AddrPort{
				netip.AddrPortFrom(current.Interface.Resolver, 53),
				netip.AddrPortFrom(netip.MustParseAddr(DNSRemoteServers[0]), 53),
				netip.AddrPortFrom(current.Interface.Gateway, 53),
			}

			warmDNS(ctx, &current.Interface, servers[:])

			dnsWarmed = true

			current.Time = time.Now()

			ticker.Reset(sampleInterval)
		}

		if ctx.Err() != nil {
			return
		}

		if gateway != current.Interface.Gateway || gatewayInterface != current.Interface.Index {
			gateway = current.Interface.Gateway
			gatewayInterface = current.Interface.Index
			gatewayDNS = false

			nextGatewayCheck = time.Time{}
		}

		checkGateway := gatewayDNS || !current.Time.Before(nextGatewayCheck)

		collectProbes(ctx, &current, checkGateway)

		if checkGateway {
			gatewayDNS = gatewayDNS || dnsResponded(&current.Probes[4])
			nextGatewayCheck = current.Time.Add(30 * time.Second)
		}

		if ctx.Err() != nil {
			return
		}

		trace := tracer.advance(ctx, current.Interface, time.Now())
		if trace != nil {
			monitor.recordTrace(trace)
		}

		application := applications.advance(ctx, current.Interface, time.Now())
		if application != nil {
			monitor.recordApplication(application)
		}

		var err error

		if writer != nil {
			err = writer.write(&current)
			if trace != nil && err == nil {
				err = writer.writeTrace(trace)
			}

			if application != nil && err == nil {
				err = writer.writeApplication(application)
			}
		}

		monitor.mu.Lock()

		if err != nil && monitor.logError == "" {
			monitor.logError = err.Error()
			log.Errorf("CSV logging stopped: %v", err)
		}

		monitor.recordChanges(&current)
		monitor.history[monitor.next] = current
		monitor.next = (monitor.next + 1) % historySize
		monitor.count = min(monitor.count+1, historySize)
		monitor.traceRunning = tracer.cancel != nil && !tracer.invalidated
		monitor.traceDue = tracer.next
		monitor.applicationRunning = applications.cancel != nil && !applications.invalidated
		monitor.version++
		monitor.mu.Unlock()

		notify()

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func collectProbes(ctx context.Context, current *sample, checkGateway bool) {
	var workers sync.WaitGroup

	state := &current.Interface
	targets := [probeCount]netip.Addr{state.Gateway, netip.MustParseAddr(ICMPRemoteTargets[0]), state.Resolver, netip.MustParseAddr(DNSRemoteServers[0]), state.Gateway}

	for index := range probeCount {
		if !state.Address.IsValid() || state.Link != "up" {
			result := probeResult{Target: addressText(targets[index]), Status: statusUnavailable, Detail: "No usable IPv4 interface"}

			if index >= 2 {
				result = unavailableDNS(targets[index], result.Detail)
			} else if index == 1 {
				result = unavailableICMP(result.Detail)
			}

			current.Probes[index] = result

			continue
		}

		workers.Go(func() {
			switch index {
			case 0:
				gateway := pingTarget(ctx, state, state.Gateway)
				current.Probes[index] = probeResult{Target: gateway.Target, Status: gateway.Status, Duration: gateway.Duration, Detail: gateway.Detail}
			case 1:
				current.Probes[index] = pingRemoteTargets(ctx, state)
			case 2:
				current.Probes[index] = queryDNS(ctx, state, state.Resolver)
			case 3:
				current.Probes[index] = queryDNS(ctx, state, targets[index])
			case 4:
				if checkGateway {
					current.Probes[index] = queryDNS(ctx, state, state.Gateway)
				} else {
					current.Probes[index] = unavailableDNS(state.Gateway, "DNS not confirmed; retrying every 30s")
				}
			}
		})
	}

	workers.Wait()
}

func outcomeCounts(status string) (int, int) {
	switch status {
	case "", statusUnavailable:
		return 0, 0
	case statusOK:
		return 1, 1
	}

	return 1, 0
}

func statusSeverity(status string) int {
	switch status {
	case statusError, statusDNSError, statusRefused, statusTLS, statusHTTP:
		return 4
	case statusPermission:
		return 3
	case statusTimeout, statusPartial:
		return 2
	case statusUnavailable:
		return 1
	}

	return 0
}

func errorStatus(err error) string {
	var networkError net.Error

	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
		return statusTimeout
	}

	if errors.Is(err, os.ErrPermission) {
		return statusPermission
	}

	return statusError
}

func addressText(address netip.Addr) string {
	if !address.IsValid() {
		return ""
	}

	return address.String()
}
