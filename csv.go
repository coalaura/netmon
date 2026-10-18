package main

import (
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type csvLog struct {
	file   *os.File
	writer *csv.Writer
	row    [55]string
	failed error
}

func (writer *csvLog) write(current *sample) error {
	if writer.failed != nil {
		return writer.failed
	}

	writer.prepare(current.Time, &current.Interface)
	row := &writer.row

	for index := range current.Probes {
		result := &current.Probes[index]

		row[12] = probeNames[index]
		row[13] = result.Target
		row[20] = "summary"

		attempted, succeeded := result.counts()

		err := writer.writeResult(result.Query, result.Status, result.Duration, result.Detail, attempted, succeeded)
		if err != nil {
			writer.failed = err

			return err
		}

		row[20] = "query"

		for queryIndex := range result.DNS {
			query := &result.DNS[queryIndex]
			if query.Query == "" {
				continue
			}

			attempted, succeeded = outcomeCounts(query.Status)

			err = writer.writeResult(query.Query, query.Status, query.Duration, query.Detail, attempted, succeeded)
			if err != nil {
				writer.failed = err

				return err
			}
		}

		row[20] = "ping"

		for targetIndex := range result.ICMP {
			target := &result.ICMP[targetIndex]
			if target.Target == "" {
				continue
			}

			row[13] = target.Target

			attempted, succeeded = outcomeCounts(target.Status)

			err = writer.writeResult("", target.Status, target.Duration, target.Detail, attempted, succeeded)
			if err != nil {
				writer.failed = err

				return err
			}
		}
	}

	err := writer.writeTraffic(current)
	if err != nil {
		return err
	}

	return writer.writeConnections(current)
}

func (writer *csvLog) prepare(timestamp time.Time, state *interfaceState) {
	clear(writer.row[:])

	row := &writer.row

	row[0] = timestamp.Format(time.RFC3339Nano)
	row[1] = strconv.Itoa(state.Index)
	row[2] = state.Name
	row[3] = addressText(state.Address)
	row[4] = state.Link
	row[5] = strconv.FormatUint(state.Speed, 10)
	row[6] = addressText(state.Gateway)
	row[7] = state.ARP
	row[8] = strconv.FormatBool(state.DefaultRoute)
	row[9] = addressText(state.Resolver)
	row[10] = state.ResolverSource
	row[11] = state.Error
	row[18] = "IPv4"
	row[19] = "7"
}

func (writer *csvLog) writeTrace(result *traceResult) error {
	if writer.failed != nil {
		return writer.failed
	}

	writer.prepare(result.Finished, &result.Interface)
	row := &writer.row

	row[12] = "Traceroute"
	row[13] = result.Target.String()
	row[20] = "trace"
	row[23] = result.Started.Format(time.RFC3339Nano)
	row[24] = result.Protocol
	row[28] = result.Change
	row[29] = strconv.FormatFloat(float64(result.Finished.Sub(result.Started))/float64(time.Millisecond), 'f', 3, 64)

	var duration time.Duration

	if result.Status == statusOK && result.HopCount > 0 {
		row[27] = strconv.Itoa(result.HopCount)

		duration = result.Hops[result.Count-1].Duration
	}

	replies := 0

	for index := range result.Count {
		if result.Hops[index].Address.IsValid() {
			replies++
		}
	}

	err := writer.writeResult("", result.Status, duration, result.Detail, result.Count, replies)

	for index := 0; index < result.Count && err == nil; index++ {
		hop := &result.Hops[index]

		row[0] = hop.Time.Format(time.RFC3339Nano)
		row[20] = "trace_hop"
		row[25] = strconv.Itoa(hop.TTL)
		row[26] = addressText(hop.Address)

		received := 0

		if hop.Address.IsValid() {
			received = 1
		}

		err = writer.writeResult("", hop.Status, hop.Duration, hop.Detail, 1, received)
	}

	if err != nil {
		writer.failed = err

		return err
	}

	writer.writer.Flush()
	writer.failed = writer.writer.Error()

	return writer.failed
}

func (writer *csvLog) writeResult(query, status string, duration time.Duration, detail string, attempted, succeeded int) error {
	row := &writer.row

	row[14] = query
	row[15] = status
	row[16] = ""

	if duration > 0 || status == statusOK || status == statusPartial || status == statusDNSError {
		row[16] = strconv.FormatFloat(float64(duration)/float64(time.Millisecond), 'f', 3, 64)
	}

	row[17] = detail
	row[21] = strconv.Itoa(attempted)
	row[22] = strconv.Itoa(succeeded)

	return writer.writer.Write(row[:])
}

func (writer *csvLog) writeTraffic(current *sample) error {
	traffic := &current.Traffic
	counters := &traffic.Counters

	writer.prepare(counters.Time, &current.Interface)
	row := &writer.row

	row[12] = "Interface traffic"
	row[20] = "traffic"
	row[15] = statusUnavailable
	row[17] = traffic.Detail

	if counters.Available {
		row[15] = "baseline"
		values := [6]uint64{counters.RXBytes, counters.TXBytes, counters.RXErrors, counters.TXErrors, counters.RXDiscards, counters.TXDiscards}

		for index, value := range values {
			row[30+index] = strconv.FormatUint(value, 10)
		}
	}

	if traffic.Valid {
		row[15] = statusOK
		row[36] = strconv.FormatFloat(traffic.RXBitsPerSecond, 'f', 3, 64)
		row[37] = strconv.FormatFloat(traffic.TXBitsPerSecond, 'f', 3, 64)
		row[38] = strconv.FormatUint(traffic.RXErrors, 10)
		row[39] = strconv.FormatUint(traffic.TXErrors, 10)
		row[40] = strconv.FormatUint(traffic.RXDiscards, 10)
		row[41] = strconv.FormatUint(traffic.TXDiscards, 10)
		row[42] = milliseconds(traffic.Interval)
	}

	writer.failed = writer.writer.Write(row[:])
	return writer.failed
}

func (writer *csvLog) writeConnections(current *sample) error {
	counts := &current.Connections

	writer.prepare(counts.Time, &current.Interface)
	row := &writer.row

	row[12] = "TCP connections"
	row[13] = "system-wide"
	row[15] = statusUnavailable
	row[17] = counts.Detail
	row[18] = "IPv4+IPv6"
	row[20] = "connections"

	if counts.Available {
		row[15] = statusOK
		row[51] = strconv.Itoa(counts.Total)
		row[52] = strconv.Itoa(counts.Established)
		row[53] = strconv.Itoa(counts.active())
		row[54] = strconv.Itoa(counts.TimeWait)
	}

	writer.failed = writer.writer.Write(row[:])
	writer.writer.Flush()

	writer.failed = errors.Join(writer.failed, writer.writer.Error())
	return writer.failed
}

func (writer *csvLog) writeApplication(current *applicationSample) error {
	if writer.failed != nil {
		return writer.failed
	}

	for index := range current.Checks {
		result := &current.Checks[index]

		writer.prepare(result.Started, &current.Interface)
		row := &writer.row

		row[12] = applicationNames[index]
		row[13] = result.Target
		row[20] = "application"
		row[43] = result.Stage

		phases := [4]phaseTiming{result.DNS, result.TCP, result.TLS, result.TTFB}

		for phaseIndex, phase := range phases {
			if phase.Measured {
				row[44+phaseIndex] = milliseconds(phase.Duration)
			}
		}

		if result.HTTPStatus != 0 {
			row[48] = strconv.Itoa(result.HTTPStatus)
		}

		row[49] = result.Remote
		row[50] = result.Finished.Format(time.RFC3339Nano)

		attempted, succeeded := outcomeCounts(result.Status)

		writer.failed = writer.writeResult("", result.Status, result.Finished.Sub(result.Started), result.Detail, attempted, succeeded)
		if writer.failed != nil {
			return writer.failed
		}
	}

	writer.writer.Flush()

	writer.failed = writer.writer.Error()
	return writer.failed
}

func (writer *csvLog) close() error {
	writer.writer.Flush()

	return errors.Join(writer.writer.Error(), writer.file.Close())
}

func openCSV(path string) (*csvLog, error) {
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		return nil, err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}

	writer := &csvLog{file: file, writer: csv.NewWriter(file)}

	header := []string{"timestamp", "interface_index", "interface", "source_ipv4", "link", "link_speed_bps", "gateway", "gateway_arp", "default_route", "interface_dns", "resolver_source", "interface_error", "monitor", "target", "query", "status", "latency_ms", "detail", "address_family", "schema_version", "record_type", "attempted", "succeeded", "trace_started", "trace_protocol", "hop", "hop_address", "hop_count", "trace_change", "trace_duration_ms", "rx_bytes", "tx_bytes", "rx_errors", "tx_errors", "rx_discards", "tx_discards", "rx_bps", "tx_bps", "rx_errors_delta", "tx_errors_delta", "rx_discards_delta", "tx_discards_delta", "counter_interval_ms", "check_stage", "dns_ms", "connect_ms", "tls_ms", "ttfb_ms", "http_status", "remote_address", "check_finished", "tcp_total", "tcp_established", "tcp_active", "tcp_time_wait"}

	err = writer.writer.Write(header)

	writer.writer.Flush()

	err = errors.Join(err, writer.writer.Error())
	if err != nil {
		file.Close()

		return nil, err
	}

	return writer, nil
}

func milliseconds(duration time.Duration) string {
	return strconv.FormatFloat(float64(duration)/float64(time.Millisecond), 'f', 3, 64)
}
