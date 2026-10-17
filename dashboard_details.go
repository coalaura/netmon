package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-gui-org/go-gui/gui"
)

type detailColumn struct {
	Title   string
	Width   float32
	Numeric bool
}

type detailCell struct {
	Text  string
	Color gui.Color
}

var (
	hostColumns    = [3]detailColumn{{Title: "HOST"}, {Title: "RESULT", Width: 96}, {Title: "RTT ms", Width: 60, Numeric: true}}
	statsColumns   = [5]detailColumn{{Title: "HOST / METRIC"}, {Title: "P50 ms", Width: 56, Numeric: true}, {Title: "P95 ms", Width: 56, Numeric: true}, {Title: "JITTER ms", Width: 56, Numeric: true}, {Title: "LOSS", Width: 56, Numeric: true}}
	phaseColumns   = [5]detailColumn{{Title: "DNS ms", Numeric: true}, {Title: "TCP ms", Numeric: true}, {Title: "TLS ms", Numeric: true}, {Title: "TTFB ms", Numeric: true}, {Title: "HTTP", Numeric: true}}
	connectColumns = [2]detailColumn{{Title: "PHASE"}, {Title: "TIME ms", Width: 60, Numeric: true}}
)

func (dashboard *dashboard) statsDetail(probeIndex int) gui.View {
	result := &dashboard.current.Probes[probeIndex]

	rows := make([]gui.View, 0, 3)

	for index := range 3 {
		host, _, _ := hostOutcome(result, index)
		if host == "" {
			continue
		}

		rows = append(rows, statisticsRow(host, &dashboard.stats[probeIndex][index]))
	}

	return detailTable(statsColumns[:], rows)
}

func (dashboard *dashboard) applicationDetails(index int) gui.View {
	result := &dashboard.application.Checks[index]

	content := make([]gui.View, 0, 4)

	if dashboard.showStats {
		metric := "TCP connect"

		if index == 1 {
			metric = "HTTPS total"
		}

		content = append(content, detailTable(statsColumns[:], []gui.View{statisticsRow(metric, &dashboard.applicationStats[index])}))
	}

	if index == 0 && !dashboard.showStats {
		cells := [2]detailCell{
			{Text: "TCP connect", Color: colorText},
			{Text: timingValue(result.TCP.Duration, result.TCP.Measured), Color: colorText},
		}

		content = append(content, detailTable(connectColumns[:], []gui.View{detailRow(connectColumns[:], cells[:], false)}))
	} else if index == 1 {
		httpStatus := "--"

		if result.HTTPStatus != 0 {
			httpStatus = strconv.Itoa(result.HTTPStatus)
		}

		cells := [5]detailCell{
			{Text: timingValue(result.DNS.Duration, result.DNS.Measured), Color: colorText},
			{Text: timingValue(result.TCP.Duration, result.TCP.Measured), Color: colorText},
			{Text: timingValue(result.TLS.Duration, result.TLS.Measured), Color: colorText},
			{Text: timingValue(result.TTFB.Duration, result.TTFB.Measured), Color: colorText},
			{Text: httpStatus, Color: statusColor(result.Status)},
		}

		content = append(content, detailTable(phaseColumns[:], []gui.View{detailRow(phaseColumns[:], cells[:], false)}))
	}

	if result.Status != statusOK {
		content = append(content, paragraph(result.Stage+": "+result.Detail, 11, statusColor(result.Status)))
	}

	if !sameApplicationNetwork(&dashboard.application.Interface, &dashboard.current.Interface) {
		content = append(content, label("Previous network", 10, colorMuted))
	}

	return gui.Column(gui.ContainerCfg{Sizing: gui.FillFit, Padding: gui.PaddingNone, Spacing: gui.SpacingPx(2), Content: content})
}

func probeDetail(result *probeResult) gui.View {
	rows := make([]gui.View, 0, 3)

	switch {
	case result.ICMPAttempted > 0:
		for index := range result.ICMP {
			target := &result.ICMP[index]
			rows = append(rows, hostRow(target.Target, target.Status, target.Duration, target.Detail))
		}
	case result.DNSAttempted > 0:
		for index := range result.DNS {
			query := &result.DNS[index]
			rows = append(rows, hostRow(query.Query, query.Status, query.Duration, query.Detail))
		}
	case result.Query == "" && result.Status == statusOK:
		rows = append(rows, hostRow(result.Target, result.Status, result.Duration, result.Detail))
	default:
		return paragraph(result.Detail, 11, statusColor(result.Status))
	}

	return detailTable(hostColumns[:], rows)
}

func hostRow(host, status string, duration time.Duration, detail string) gui.View {
	text := strings.ToUpper(status)

	if status == statusDNSError {
		text = detail
	}

	cells := [3]detailCell{
		{Text: host, Color: colorText},
		{Text: text, Color: statusColor(status)},
		{Text: timingValue(duration, status == statusOK), Color: statusColor(status)},
	}

	return detailRow(hostColumns[:], cells[:], false)
}

func statisticsRow(host string, stats *latencySummary) gui.View {
	lossColor := colorMuted

	if stats.Failed > 0 {
		lossColor = colorAmber
	}

	cells := [5]detailCell{
		{Text: host, Color: colorText},
		{Text: timingValue(stats.Median, stats.Samples > 0), Color: colorText},
		{Text: timingValue(stats.P95, stats.Samples > 0), Color: colorText},
		{Text: timingValue(stats.Jitter, stats.JitterPairs > 0), Color: colorText},
		{Text: failureText(stats), Color: lossColor},
	}

	return detailRow(statsColumns[:], cells[:], false)
}

func detailTable(columns []detailColumn, rows []gui.View) gui.View {
	var headers [5]detailCell

	for index, column := range columns {
		headers[index] = detailCell{Text: column.Title, Color: colorMuted}
	}

	content := make([]gui.View, 0, len(rows)+1)
	content = append(content, detailRow(columns, headers[:len(columns)], true))
	content = append(content, rows...)

	return gui.Column(gui.ContainerCfg{Sizing: gui.FillFit, Padding: gui.PaddingNone, Spacing: gui.SpacingPx(1), Content: content})
}

func detailRow(columns []detailColumn, cells []detailCell, header bool) gui.View {
	content := make([]gui.View, 0, len(columns))

	size := float32(11)
	background := gui.Color{}

	if header {
		size = 10
		background = colorChart
	}

	for index, column := range columns {
		align := gui.TextAlignLeft
		sizing := gui.FillFit

		if column.Numeric {
			align = gui.TextAlignRight
		}

		if column.Width > 0 {
			sizing = gui.FixedFit
		}

		cell := &cells[index]
		text := gui.Text(gui.TextCfg{Text: cell.Text, TextStyle: gui.TextStyle{Size: size, Color: cell.Color, Align: align}, Sizing: gui.FillFit, Mode: gui.TextModeWrap})

		content = append(content, gui.Column(gui.ContainerCfg{Sizing: sizing, Width: column.Width, Padding: gui.PaddingNone, Content: []gui.View{text}}))
	}

	return gui.Row(gui.ContainerCfg{Sizing: gui.FillFit, Padding: gui.PadVH(1, 4), Spacing: gui.SpacingPx(6), Color: background, VAlign: gui.VAlignMiddle, Content: content})
}

func timingValue(duration time.Duration, measured bool) string {
	if !measured {
		return "--"
	}

	return fmt.Sprintf("%.1f", float64(duration)/float64(time.Millisecond))
}
