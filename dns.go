package main

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const dnsTargetCount = 3

type dnsQueryResult struct {
	Query    string
	Status   string
	Duration time.Duration
	Detail   string
}

var (
	DNSRemoteTargets = [dnsTargetCount]string{
		"example.com",
		"google.com",
		"cloudflare.com",
	}

	DNSRemoteServers = []string{
		"9.9.9.9",
		"1.1.1.1",
		"8.8.8.8",
	}

	dnsQueries = strings.Join(DNSRemoteTargets[:], ";")
)

func warmDNS(ctx context.Context, state *interfaceState, servers []netip.AddrPort) {
	var workers sync.WaitGroup

	for index, server := range servers {
		if !server.IsValid() || slices.Contains(servers[:index], server) {
			continue
		}

		workers.Go(func() {
			// Prime resolver caches once; warm-up results never enter monitoring history or CSV.
			queryDNSEndpoint(ctx, state, server)
		})
	}

	workers.Wait()
}

func queryDNS(ctx context.Context, state *interfaceState, server netip.Addr) probeResult {
	return queryDNSEndpoint(ctx, state, netip.AddrPortFrom(server, 53))
}

func queryDNSEndpoint(ctx context.Context, state *interfaceState, endpoint netip.AddrPort) probeResult {
	result := unavailableDNS(endpoint.Addr(), "No IPv4 DNS server available")

	if !endpoint.Addr().IsValid() {
		return result
	}

	var workers sync.WaitGroup

	for index, query := range DNSRemoteTargets {
		workers.Go(func() {
			result.DNS[index] = exchangeDNS(ctx, state, endpoint, query)
		})
	}

	workers.Wait()

	summarizeDNS(&result)

	return result
}

func exchangeDNS(ctx context.Context, state *interfaceState, endpoint netip.AddrPort, query string) dnsQueryResult {
	server := endpoint.Addr()
	result := dnsQueryResult{Query: query, Status: statusUnavailable}

	if !server.IsValid() {
		result.Detail = "No IPv4 DNS server available"

		return result
	}

	queryContext, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	message := dns.Msg{}
	message.SetQuestion(dns.Fqdn(query), dns.TypeA)

	client := dns.Client{Net: "udp4", Timeout: probeTimeout, Dialer: &net.Dialer{Timeout: probeTimeout}}

	if !server.IsLoopback() {
		client.Dialer.LocalAddr = &net.UDPAddr{IP: net.IP(state.Address.AsSlice())}
	}

	started := time.Now()
	transport := "UDP"

	response, _, err := client.ExchangeContext(queryContext, &message, endpoint.String())
	if err == nil && response.Truncated {
		transport = "TCP"
		client.Net = "tcp4"

		if !server.IsLoopback() {
			client.Dialer.LocalAddr = &net.TCPAddr{IP: net.IP(state.Address.AsSlice())}
		}

		response, _, err = client.ExchangeContext(queryContext, &message, endpoint.String())
	}

	if err != nil {
		result.Status = errorStatus(err)
		result.Detail = err.Error()

		return result
	}

	result.Duration = time.Since(started)
	result.Status = statusDNSError

	if !validDNSResponse(response, &message) {
		result.Status = statusError
		result.Detail = "Mismatched DNS response"

		return result
	}

	if response.Rcode != dns.RcodeSuccess {
		result.Detail = transport + " " + dns.RcodeToString[response.Rcode]

		return result
	}

	if response.Truncated {
		result.Detail = "Truncated TCP response"

		return result
	}

	var answers strings.Builder

	answers.Grow(96)
	answers.WriteString(transport)
	answers.WriteString(" NOERROR")

	count := 0

	for _, answer := range response.Answer {
		if address, ok := answer.(*dns.A); ok {
			count++
			if count <= 3 {
				answers.WriteString(" ")
				answers.WriteString(address.A.String())
			}
		}
	}

	if count == 0 {
		result.Detail = transport + " NOERROR; no A records"

		return result
	}

	if count > 3 {
		answers.WriteString(" +")
		answers.WriteString(strconv.Itoa(count - 3))
	}

	result.Status = statusOK
	result.Detail = answers.String()

	return result
}

func unavailableDNS(server netip.Addr, detail string) probeResult {
	result := probeResult{Target: addressText(server), Query: dnsQueries, Status: statusUnavailable, Detail: detail}

	for index, query := range DNSRemoteTargets {
		result.DNS[index] = dnsQueryResult{Query: query, Status: statusUnavailable, Detail: detail}
	}

	return result
}

func summarizeDNS(result *probeResult) {
	result.Status = statusUnavailable
	result.Duration = 0
	result.DNSAttempted = 0
	result.DNSSucceeded = 0

	var detail strings.Builder

	detail.Grow(160)

	for index := range result.DNS {
		query := &result.DNS[index]

		if index > 0 {
			detail.WriteString(" | ")
		}

		detail.WriteString(query.Query)
		detail.WriteByte(' ')

		if query.Status != statusUnavailable {
			result.DNSAttempted++
		}

		if query.Status == statusOK {
			result.DNSSucceeded++
			result.Duration += query.Duration

			detail.WriteString(strconv.FormatFloat(float64(query.Duration)/float64(time.Millisecond), 'f', 1, 64))
			detail.WriteString("ms")
		} else {
			detail.WriteString(query.Status)
		}

		if statusSeverity(query.Status) > statusSeverity(result.Status) {
			result.Status = query.Status
		}
	}

	if result.DNSSucceeded > 0 {
		result.Duration /= time.Duration(result.DNSSucceeded)
		result.Status = statusPartial

		if result.DNSSucceeded == dnsTargetCount {
			result.Status = statusOK
		}
	}

	result.Detail = detail.String()
}

func dnsResponded(result *probeResult) bool {
	for index := range result.DNS {
		status := result.DNS[index].Status
		if status == statusOK || status == statusDNSError {
			return true
		}
	}

	return false
}

func validDNSResponse(response, request *dns.Msg) bool {
	if !response.Response || response.Opcode != dns.OpcodeQuery || len(response.Question) != 1 {
		return false
	}

	question := &response.Question[0]
	expected := &request.Question[0]

	return question.Qtype == expected.Qtype && question.Qclass == expected.Qclass && strings.EqualFold(question.Name, expected.Name)
}
