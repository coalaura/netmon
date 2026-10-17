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
	row    [30]string
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

	writer.writer.Flush()
	writer.failed = writer.writer.Error()
	return writer.failed
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
	row[19] = "4"
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
	header := []string{"timestamp", "interface_index", "interface", "source_ipv4", "link", "link_speed_bps", "gateway", "gateway_arp", "default_route", "interface_dns", "resolver_source", "interface_error", "monitor", "target", "query", "status", "latency_ms", "detail", "address_family", "schema_version", "record_type", "attempted", "succeeded", "trace_started", "trace_protocol", "hop", "hop_address", "hop_count", "trace_change", "trace_duration_ms"}
	err = writer.writer.Write(header)
	writer.writer.Flush()

	err = errors.Join(err, writer.writer.Error())
	if err != nil {
		file.Close()

		return nil, err
	}

	return writer, nil
}
