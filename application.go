package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

const (
	applicationInterval = 2 * time.Second
	applicationTimeout  = 1500 * time.Millisecond
	tcpTarget           = "1.1.1.1:443"
	httpsTarget         = "https://www.gstatic.com/generate_204"
	statusRefused       = "refused"
	statusTLS           = "tls_error"
	statusHTTP          = "http_error"
)

type phaseTiming struct {
	Duration time.Duration
	Measured bool
}

type applicationResult struct {
	Started    time.Time
	Finished   time.Time
	Target     string
	Remote     string
	Status     string
	Stage      string
	Detail     string
	DNS        phaseTiming
	TCP        phaseTiming
	TLS        phaseTiming
	TTFB       phaseTiming
	HTTPStatus int
}

type applicationSample struct {
	Interface interfaceState
	Checks    [2]applicationResult
}

type applicationSampler struct {
	results     chan *applicationSample
	cancel      context.CancelFunc
	state       interfaceState
	next        time.Time
	invalidated bool
}

var applicationNames = [2]string{"TCP 443", "HTTPS"}

func (sampler *applicationSampler) advance(ctx context.Context, state interfaceState, now time.Time) *applicationSample {
	changed := !sameApplicationNetwork(&sampler.state, &state)
	if changed {
		sampler.next = time.Time{}
		if sampler.cancel != nil {
			sampler.invalidated = true

			sampler.cancel()
		}
	}

	var completed *applicationSample

	if sampler.cancel != nil {
		select {
		case completed = <-sampler.results:
			sampler.cancel()

			sampler.cancel = nil

			if sampler.invalidated {
				invalidateApplication(completed)
			}
		default:
		}
	}

	if sampler.cancel == nil && !now.Before(sampler.next) && state.Link == "up" && state.Address.Is4() && state.DefaultRoute && ctx.Err() == nil {
		if sampler.results == nil {
			sampler.results = make(chan *applicationSample, 1)
		}

		checkContext, cancel := context.WithCancel(ctx)

		sampler.cancel = cancel
		sampler.invalidated = false
		sampler.state = state
		sampler.next = now.Add(applicationInterval)

		go func() {
			sampler.results <- checkApplication(checkContext, state)
		}()
	}

	return completed
}

func (sampler *applicationSampler) stop() *applicationSample {
	if sampler.cancel == nil {
		return nil
	}

	sampler.cancel()

	result := <-sampler.results

	sampler.cancel = nil

	if sampler.invalidated {
		invalidateApplication(result)
	}

	return result
}

func checkApplication(ctx context.Context, state interfaceState) *applicationSample {
	result := &applicationSample{Interface: state}

	var workers sync.WaitGroup

	workers.Go(func() {
		result.Checks[0] = checkTCP(ctx, &state, tcpTarget)
	})

	workers.Go(func() {
		result.Checks[1] = checkHTTPS(ctx, &state, httpsTarget, http.StatusNoContent, nil)
	})

	workers.Wait()

	return result
}

func checkTCP(parent context.Context, state *interfaceState, target string) applicationResult {
	ctx, cancel := context.WithTimeout(parent, applicationTimeout)
	defer cancel()

	result := applicationResult{Started: time.Now(), Target: target, Stage: "TCP"}
	dialer := applicationDialer(state)

	connection, err := dialer.DialContext(ctx, "tcp4", target)

	result.TCP = phaseTiming{Duration: time.Since(result.Started), Measured: true}

	if err == nil {
		result.Remote = connection.RemoteAddr().String()
		connection.Close()
	}

	finishApplication(&result, err)

	return result
}

func checkHTTPS(parent context.Context, state *interfaceState, target string, expectedStatus int, tlsConfig *tls.Config) applicationResult {
	ctx, cancel := context.WithTimeout(parent, applicationTimeout)
	defer cancel()

	result := applicationResult{Started: time.Now(), Target: target, Stage: "request"}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		finishApplication(&result, err)

		return result
	}

	if request.URL.Scheme != "https" {
		finishApplication(&result, errors.New("HTTPS endpoint required"))

		return result
	}

	host := request.URL.Hostname()

	port := request.URL.Port()
	if port == "" {
		port = "443"
	}

	address, parseError := netip.ParseAddr(host)
	if parseError != nil {
		result.Stage = "DNS"
		started := time.Now()

		address, err = applicationAddress(ctx, state, netip.AddrPortFrom(state.Resolver, 53), host)

		result.DNS = phaseTiming{Duration: time.Since(started), Measured: true}

		if err != nil {
			finishApplication(&result, err)

			return result
		}
	}

	result.Stage = "TCP"

	dialer := applicationDialer(state)

	started := time.Now()

	connection, err := dialer.DialContext(ctx, "tcp4", net.JoinHostPort(address.String(), port))

	result.TCP = phaseTiming{Duration: time.Since(started), Measured: true}

	if err != nil {
		finishApplication(&result, err)

		return result
	}

	defer connection.Close()

	// Direct socket I/O needs cancellation in addition to its absolute deadline.
	stop := context.AfterFunc(ctx, func() {
		connection.Close()
	})

	defer stop()

	deadline, _ := ctx.Deadline()

	err = connection.SetDeadline(deadline)
	if err != nil {
		finishApplication(&result, err)

		return result
	}

	result.Remote = connection.RemoteAddr().String()

	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		tlsConfig = tlsConfig.Clone()
	}

	tlsConfig.ServerName = host
	tlsConfig.NextProtos = []string{"http/1.1"}

	secure := tls.Client(connection, tlsConfig)

	result.Stage = "TLS"
	started = time.Now()

	err = secure.HandshakeContext(ctx)

	result.TLS = phaseTiming{Duration: time.Since(started), Measured: true}

	if err != nil {
		finishApplication(&result, applicationContextError(ctx, err))

		return result
	}

	result.Stage = "HTTP write"

	request.Close = true
	request.Header.Set("User-Agent", "Netmon/1 network-diagnostics")
	request.Header.Set("Cache-Control", "no-cache")

	err = request.Write(secure)
	if err != nil {
		finishApplication(&result, applicationContextError(ctx, err))

		return result
	}

	result.Stage = "TTFB"
	started = time.Now()

	// Bound response header memory and stop after headers; this is not a download test.
	reader := bufio.NewReader(io.LimitReader(secure, 64*1024))

	_, err = reader.Peek(1)

	result.TTFB = phaseTiming{Duration: time.Since(started), Measured: true}

	if err != nil {
		finishApplication(&result, applicationContextError(ctx, err))

		return result
	}

	result.Stage = "HTTP"

	response, err := http.ReadResponse(reader, request)
	if err != nil {
		finishApplication(&result, applicationContextError(ctx, err))

		return result
	}

	// Close the connection before Body.Close to avoid draining an unexpected large body.
	connection.Close()
	response.Body.Close()

	result.HTTPStatus = response.StatusCode

	finishApplication(&result, nil)

	result.Detail = fmt.Sprintf("HTTP %d; direct IPv4; fresh TCP/TLS connection", response.StatusCode)

	if response.StatusCode != expectedStatus {
		result.Status = statusHTTP
		result.Stage = "HTTP"
		result.Detail = fmt.Sprintf("HTTP %d; expected %d; redirects are not followed", response.StatusCode, expectedStatus)
	}

	return result
}

func applicationAddress(ctx context.Context, state *interfaceState, endpoint netip.AddrPort, host string) (netip.Addr, error) {
	if !endpoint.Addr().Is4() {
		return netip.Addr{}, errors.New("no IPv4 interface DNS resolver")
	}

	resolver := net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		dialer := net.Dialer{}

		if !endpoint.Addr().IsLoopback() {
			if network == "udp" {
				dialer.LocalAddr = &net.UDPAddr{IP: net.IP(state.Address.AsSlice())}
			} else {
				dialer.LocalAddr = &net.TCPAddr{IP: net.IP(state.Address.AsSlice())}
			}
		}

		return dialer.DialContext(ctx, network+"4", endpoint.String())
	}}

	addresses, err := resolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return netip.Addr{}, err
	}

	if len(addresses) == 0 {
		return netip.Addr{}, errors.New("DNS returned no IPv4 addresses")
	}

	return addresses[0].Unmap(), nil
}

func applicationDialer(state *interfaceState) net.Dialer {
	return net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IP(state.Address.AsSlice())}, KeepAlive: -1}
}

func finishApplication(result *applicationResult, err error) {
	result.Finished = time.Now()
	result.Status = statusOK
	result.Detail = "Connection established"

	if err == nil {
		result.Stage = "complete"

		return
	}

	result.Status = errorStatus(err)
	result.Detail = err.Error()

	if errors.Is(err, context.Canceled) {
		result.Status = statusCancelled
	} else if errors.Is(err, connectionRefusedError) {
		result.Status = statusRefused
	} else if result.Status == statusError && result.Stage == "TLS" {
		result.Status = statusTLS
	}
}

func applicationContextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	return err
}

func sameApplicationNetwork(first, second *interfaceState) bool {
	return sameTraceNetwork(first, second) && first.Resolver == second.Resolver
}

func invalidateApplication(result *applicationSample) {
	for index := range result.Checks {
		result.Checks[index].Status = statusCancelled
		result.Checks[index].Detail = "Interface, route, or resolver changed during check"
	}
}
