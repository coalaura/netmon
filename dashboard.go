package main

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-gui-org/go-charts/axis"
	"github.com/go-gui-org/go-charts/chart"
	"github.com/go-gui-org/go-charts/series"
	"github.com/go-gui-org/go-charts/theme"
	"github.com/go-gui-org/go-gui/gui"
)

const chartWindow = 5 * time.Minute

type fixedTimeAxis struct {
	*axis.Time
}

type fixedValueAxis struct {
	*axis.Linear
}

type dashboard struct {
	series             [2][]series.XY
	regions            [2][]chart.RegionAnnotation
	start              time.Time
	end                time.Time
	events             []string
	traceDue           time.Time
	tracePoints        []series.Point
	traceSeries        []series.XY
	traceRegions       []chart.RegionAnnotation
	logError           string
	csvOpenError       string
	monitor            *monitor
	current            sample
	version            uint64
	points             [probeCount][]series.Point
	chartTheme         *theme.Theme
	loss               [probeCount]string
	trace              *traceResult
	csvOpening         bool
	traceRunning       bool
	application        *applicationSample
	applicationRunning bool
	frozen             bool
	showStats          bool
	stats              [probeCount][3]latencySummary
	applicationStats   [2]latencySummary
	applicationPoints  [2][]series.Point
	applicationSeries  []series.XY
	applicationRegions []chart.RegionAnnotation
	connectionPoints   [2][]series.Point
	connectionSeries   []series.XY
}

var (
	colorBackground = gui.Hex(0x121212)
	colorPanel      = gui.Hex(0x1B1B1B)
	colorChart      = gui.Hex(0x1E1E1E)
	colorControl    = gui.Hex(0x202020)
	colorHover      = gui.Hex(0x2A2A2A)
	colorBorder     = gui.Hex(0x333333)
	colorGrid       = gui.Hex(0x2B2B2B)
	colorSubtle     = gui.Hex(0x818181)
	colorText       = gui.Hex(0xDADADA)
	colorMuted      = gui.Hex(0x989898)
	colorGreen      = gui.Hex(0x76D6A1)
	colorTeal       = gui.Hex(0x45CDBF)
	colorSand       = gui.Hex(0xE1BD70)
	colorAmber      = gui.Hex(0xE9B04F)
	colorRed        = gui.Hex(0xEF7373)
	colorOrange     = gui.Hex(0xE79559)
	probeColors     = [probeCount]gui.Color{colorGreen, colorSand, colorGreen, colorSand, colorTeal}
)

func (*fixedTimeAxis) SetRange(_, _ float64) {
	// go-charts replaces even explicit time bounds with series bounds on each draw.
	// Keep the supplied rolling window instead, including when samples are missing.
}

func (*fixedValueAxis) SetRange(_, _ float64) {
	// Preserve the zero baseline and headroom instead of fitting only the observed values.
}

func (dashboard *dashboard) refresh() {
	if dashboard.frozen {
		return
	}

	monitor := dashboard.monitor

	monitor.mu.RLock()
	defer monitor.mu.RUnlock()

	if monitor.count == 0 || monitor.version == dashboard.version {
		return
	}

	dashboard.current = monitor.history[(monitor.next+historySize-1)%historySize]
	dashboard.version = monitor.version
	dashboard.logError = monitor.logError
	dashboard.end = dashboard.current.Time
	dashboard.start = dashboard.end.Add(-chartWindow)

	dashboard.application = monitor.application
	dashboard.applicationRunning = monitor.applicationRunning
	dashboard.events = dashboard.events[:0]

	for offset := range monitor.eventCount {
		index := (monitor.eventNext - monitor.eventCount + offset + len(monitor.events)) % len(monitor.events)
		dashboard.events = append(dashboard.events, monitor.events[index])
	}

	dashboard.refreshTraces()
	dashboard.refreshStats()
	dashboard.refreshApplications()
	dashboard.refreshConnections()

	for index := range dashboard.points {
		dashboard.points[index] = dashboard.points[index][:0]
	}

	for index := range dashboard.regions {
		dashboard.regions[index] = dashboard.regions[index][:0]
	}

	var (
		failed    [probeCount]int
		attempted [probeCount]int
	)

	for offset := range monitor.count {
		index := (monitor.next - monitor.count + offset + historySize) % historySize

		current := &monitor.history[index]
		if current.Time.Before(dashboard.start) {
			continue
		}

		coordinate := float64(current.Time.UnixNano()) / float64(time.Second)

		for probeIndex := range current.Probes {
			result := &current.Probes[probeIndex]
			value := math.NaN()

			if result.Status == statusOK || result.Status == statusPartial {
				value = float64(result.Duration) / float64(time.Millisecond)
			}

			attempts, successes := result.counts()

			attempted[probeIndex] += attempts
			failed[probeIndex] += attempts - successes

			dashboard.points[probeIndex] = append(dashboard.points[probeIndex], series.Point{X: coordinate, Y: value})
		}

		dashboard.appendRegion(0, coordinate, current.Probes[:2])
		dashboard.appendRegion(1, coordinate, current.Probes[2:])
	}

	for index := range dashboard.loss {
		dashboard.loss[index] = "--"

		if attempted[index] > 0 {
			dashboard.loss[index] = fmt.Sprintf("%.1f%%", 100*float64(failed[index])/float64(attempted[index]))
		}
	}

	dashboard.buildSeries(0, 0, 2)
	dashboard.buildSeries(1, 2, 5)
}

func (dashboard *dashboard) buildSeries(group, first, last int) {
	data := dashboard.series[group][:0]

	for index := first; index < last; index++ {
		points := dashboard.points[index]
		start := 0

		// go-charts skips NaNs without breaking its polyline; use separate contiguous series.
		for offset := 0; offset <= len(points); offset++ {
			boundary := offset == len(points)
			if !boundary {
				boundary = math.IsNaN(points[offset].Y)

				if offset > start && points[offset].X-points[offset-1].X > 1.5 {
					boundary = true
				}
			}

			if !boundary {
				continue
			}

			if offset > start {
				data = append(data, series.NewXY(series.XYCfg{Name: probeNames[index], Color: probeColors[index], Points: points[start:offset]}))
			}

			start = offset

			if offset < len(points) && math.IsNaN(points[offset].Y) {
				start++
			}
		}
	}

	dashboard.series[group] = data
}

func (dashboard *dashboard) appendRegion(index int, coordinate float64, results []probeResult) {
	status := statusOK

	for resultIndex := range results {
		candidate := results[resultIndex].Status
		if statusSeverity(candidate) > statusSeverity(status) {
			status = candidate
		}
	}

	if status == statusOK || status == statusUnavailable {
		return
	}

	color := statusColor(status)
	color.A = 38

	regions := dashboard.regions[index]
	if len(regions) > 0 && regions[len(regions)-1].Color == color && coordinate-regions[len(regions)-1].Max < 0.75 {
		regions[len(regions)-1].Max = coordinate + 0.5

		dashboard.regions[index] = regions

		return
	}

	dashboard.regions[index] = append(regions, chart.RegionAnnotation{Axis: chart.AnnotationX, Min: coordinate - 0.5, Max: coordinate + 0.5, Color: color})
}

func (dashboard *dashboard) view(_ *gui.Window) gui.View {
	state := &dashboard.current.Interface
	updated := "Discovering network..."

	if !dashboard.current.Time.IsZero() {
		updated = "LIVE  |  " + dashboard.current.Time.Format("15:04:05") + "  |  IPv4  |  1s sampling"
	}

	freezeLabel := "Freeze display"
	statsLabel := "Stats"
	headerColor := colorGreen

	if dashboard.frozen {
		updated = "FROZEN  |  " + dashboard.end.Format("15:04:05") + "  |  sampling and CSV continue"
		freezeLabel = "Resume live"
		headerColor = colorAmber
	}

	if dashboard.showStats {
		statsLabel = "Stats: on"
	}

	interfaceName := state.Name
	if interfaceName == "" {
		interfaceName = "No active interface"
	}

	route := "Missing"
	routeColor := colorRed

	if state.DefaultRoute {
		route = "Present"
		routeColor = colorGreen
	}

	linkColor := colorAmber

	if state.Link == "up" {
		linkColor = colorGreen
	}

	content := []gui.View{
		gui.Row(gui.ContainerCfg{
			Sizing:  gui.FillFit,
			Padding: gui.PaddingNone,
			Spacing: gui.SpacingPx(12),
			VAlign:  gui.VAlignMiddle,
			Content: []gui.View{
				label("NETMON", 16, colorText),
				paragraph(updated, 11, headerColor),
				controlButton("stats", statsLabel, dashboard.toggleStats),
				controlButton("freeze", freezeLabel, dashboard.toggleFreeze),
			},
		}),
		gui.Row(gui.ContainerCfg{
			Sizing:  gui.FillFit,
			Padding: gui.PaddingNone,
			Spacing: gui.SpacingPx(6),
			Content: []gui.View{
				metricCard("INTERFACE", interfaceName, addressText(state.Address), colorText),
				metricCard("LINK", strings.ToUpper(state.Link), "Negotiated speed: "+speedText(state.Speed), linkColor),
				metricCard("GATEWAY / ARP", addressText(state.Gateway), "Neighbor: "+state.ARP, neighborColor(state.ARP)),
				metricCard("DEFAULT ROUTE", route, "DNS: "+addressText(state.Resolver), routeColor),
			},
		}),
		dashboard.trafficRow(),
		gui.Row(gui.ContainerCfg{
			Sizing:  gui.FillFit,
			Padding: gui.PaddingNone,
			Spacing: gui.SpacingPx(6),
			Content: []gui.View{dashboard.latencyChart(0, 2, "ICMP RTT / remote mean", "icmp"), dashboard.latencyChart(2, 5, "DNS mean", "dns")},
		}),
		gui.Row(gui.ContainerCfg{
			Sizing:  gui.FillFit,
			Padding: gui.PaddingNone,
			Spacing: gui.SpacingPx(6),
			Content: []gui.View{dashboard.applicationChart(), dashboard.connectionChart()},
		}),
		paragraph("5 MIN WINDOW  |  Amber: timeout / partial results; red: error; orange: permission. Means include successful replies only; gaps: none succeeded.", 11, colorMuted),
		dashboard.probeTable(),
		paragraph("DNS: example.com / google.com / cloudflare.com concurrently; mean of successful hosts. Resolver: "+state.ResolverSource, 11, colorMuted),
		paragraph("Latency: ICMP RTT / DNS mean / TCP connect / HTTPS total. TCP and HTTPS: fresh connections every 2s, 1.5s deadline.", 11, colorMuted),
		dashboard.tracePanel(),
	}

	if state.Error != "" {
		content = append(content, paragraph(state.Error, 12, colorAmber))
	}

	content = append(content, dashboard.eventPanel())

	logging := "CSV  " + dashboard.monitor.logPath
	loggingColor := colorMuted

	if dashboard.logError != "" {
		logging = "CSV ERROR  " + dashboard.logError + "  |  " + dashboard.monitor.logPath
		loggingColor = colorRed
	}

	content = append(content, gui.Row(gui.ContainerCfg{
		Sizing:  gui.FillFit,
		Padding: gui.PaddingNone,
		Spacing: gui.SpacingPx(8),
		Content: []gui.View{
			paragraph(logging, 12, loggingColor),
			gui.Button(gui.ButtonCfg{
				ID:       "open-csv",
				Sizing:   gui.FitFit,
				Padding:  gui.PadVH(4, 8),
				Radius:   gui.RadiusPx(0),
				Colors:   gui.ColorSet{Base: colorControl, Hover: colorHover, Click: colorBackground, Focus: colorHover, BorderFocus: colorMuted, Border: colorBorder},
				Disabled: dashboard.csvOpening,
				Content:  []gui.View{label("Open CSV", 11, colorText)},
				OnClick:  dashboard.openCSV,
			}),
		},
	}))

	if dashboard.csvOpenError != "" {
		content = append(content, paragraph("Cannot open CSV: "+dashboard.csvOpenError, 11, colorRed))
	}

	return gui.Column(gui.ContainerCfg{
		ID:         "dashboard",
		Sizing:     gui.FillFill,
		Color:      colorBackground,
		Padding:    gui.PadAll(8),
		Spacing:    gui.SpacingPx(6),
		Scrollable: true,
		Content:    content,
	})
}

func (dashboard *dashboard) latencyChart(first, last int, title, id string) gui.View {
	legend := make([]gui.View, 0, last-first)
	maximum := float64(10)

	for index := first; index < last; index++ {
		legend = append(legend, label(probeNames[index], 11, probeColors[index]))

		for _, point := range dashboard.points[index] {
			if !math.IsNaN(point.Y) {
				maximum = max(maximum, point.Y*1.15)
			}
		}
	}

	regionIndex := 0

	if first != 0 {
		regionIndex = 1
	}

	plot := lineChart(chart.LineCfg{
		ID:            id,
		Title:         title,
		Sizing:        gui.FillFixed,
		Height:        180,
		Theme:         dashboard.chartTheme,
		Version:       dashboard.version,
		TooltipXLabel: "Time",
		TooltipYLabel: "ms",
		Annotations:   chart.Annotations{Regions: dashboard.regions[regionIndex]},
		Series:        dashboard.series[regionIndex],
		XAxis:         &fixedTimeAxis{Time: axis.NewTime(axis.TimeCfg{Min: dashboard.start, Max: dashboard.end, Format: "15:04:05"})},
		YAxis:         &fixedValueAxis{Linear: axis.NewLinear(axis.LinearCfg{Title: "ms", Min: 0, Max: maximum, TickCount: 5})},
		LineWidth:     1.5,
		ShowMarkers:   false,
	}, "No successful samples in this window")

	return gui.Column(gui.ContainerCfg{
		Sizing:  gui.FillFit,
		Padding: gui.PaddingNone,
		Spacing: gui.SpacingPx(2),
		Content: []gui.View{gui.Row(gui.ContainerCfg{Padding: gui.PaddingNone, Spacing: gui.SpacingPx(10), Content: legend}), plot},
	})
}

func (dashboard *dashboard) probeTable() gui.View {
	rows := make([]gui.View, 0, probeCount+len(applicationNames)+1)

	detailTitle := "HOST RESULTS / DETAIL"

	if dashboard.showStats {
		detailTitle = "HOST: P50 / P95 / JITTER (ms) / LOSS | 5M, CURRENT INTERFACE / TARGETS"
	}

	rows = append(rows, probeRow([]gui.View{
		fixedLabel("MONITOR", 110, colorMuted),
		fixedLabel("RESULT", 94, colorMuted),
		fixedLabel("LATENCY", 84, colorMuted),
		fixedLabel("FAIL / 5M", 72, colorMuted),
		fixedLabel("TARGET", 108, colorMuted),
		paragraph(detailTitle, 10, colorMuted),
	}))

	order := [probeCount]int{0, 1, 2, 4, 3}

	for _, index := range order {
		result := &dashboard.current.Probes[index]

		status := strings.ToUpper(result.Status)
		if status == "" {
			status = "WAITING"
		}

		if result.DNSAttempted > 0 {
			status = fmt.Sprintf("%d/%d OK", result.DNSSucceeded, result.DNSAttempted)
		} else if result.ICMPAttempted > 0 {
			status = fmt.Sprintf("%d/%d OK", result.ICMPSucceeded, result.ICMPAttempted)
		}

		target := result.Target

		if result.ICMP[0].Target != "" {
			target = "3 hosts"
		}

		latency := "--"

		if result.Duration > 0 {
			latency = strconv.FormatFloat(float64(result.Duration)/float64(time.Millisecond), 'f', 1, 64) + " ms"
		}

		detail := probeDetail(result)

		if dashboard.showStats {
			detail = dashboard.statsDetail(index)
		}

		rows = append(rows, probeRow([]gui.View{
			fixedLabel(probeNames[index], 110, probeColors[index]),
			fixedLabel(status, 94, statusColor(result.Status)),
			fixedLabel(latency, 84, colorText),
			fixedLabel(dashboard.loss[index], 72, colorMuted),
			fixedLabel(target, 108, colorText),
			detail,
		}))
	}

	rows = dashboard.appendApplicationRows(rows)

	return panel(rows)
}

func (dashboard *dashboard) eventPanel() gui.View {
	rows := make([]gui.View, 0, max(1, len(dashboard.events)))

	for _, message := range slices.Backward(dashboard.events) {
		rows = append(rows, label(message, 11, colorMuted))
	}

	if len(dashboard.events) == 0 {
		rows = append(rows, label("Waiting for the first sample...", 11, colorMuted))
	}

	return panel([]gui.View{
		label("RECENT CHANGES", 13, colorText),
		gui.Column(gui.ContainerCfg{
			ID:            "recent-changes",
			Sizing:        gui.FillFixed,
			Height:        90,
			Padding:       gui.PaddingNone,
			Spacing:       gui.SpacingPx(3),
			Scrollable:    true,
			ClipContents:  true,
			ScrollbarCfgX: &gui.ScrollbarCfg{Size: 5, Radius: gui.RadiusPx(0), ColorThumb: colorSubtle, ColorBackground: colorPanel},
			ScrollbarCfgY: &gui.ScrollbarCfg{Size: 6, Radius: gui.RadiusPx(0), ColorThumb: colorSubtle, ColorBackground: colorPanel},
			Content:       rows,
		}),
	})
}

func (dashboard *dashboard) openCSV(event gui.EventCtx) {
	if dashboard.csvOpening {
		return
	}

	dashboard.csvOpening = true
	dashboard.csvOpenError = ""

	window := event.Window

	path := dashboard.monitor.logPath

	window.InvalidateLayout()

	go func() {
		err := openLogFile(path)
		if err != nil {
			log.Errorf("Cannot open CSV %s: %v", path, err)
		}

		if window.Ctx().Err() != nil {
			return
		}

		window.QueueCommand(func(window *gui.Window) {
			dashboard.csvOpening = false

			if err != nil {
				dashboard.csvOpenError = err.Error()
			}

			window.InvalidateLayout()
		})
	}()
}

func newDashboard(monitor *monitor) *dashboard {
	chartTheme := theme.Default()

	chartTheme.Background = colorChart
	chartTheme.TitleStyle = gui.TextStyle{Size: 13, Color: colorText}
	chartTheme.LabelStyle = gui.TextStyle{Size: 11, Color: colorMuted}
	chartTheme.TickStyle = gui.TextStyle{Size: 10, Color: colorMuted}
	chartTheme.TooltipStyle = gui.TextStyle{Size: 11, Color: colorText}
	chartTheme.GridColor = colorGrid
	chartTheme.AxisColor = colorSubtle
	chartTheme.Legend.TextStyle = gui.TextStyle{Size: 10, Color: colorText}
	chartTheme.Legend.Background = colorPanel
	chartTheme.Legend.Position = theme.LegendNone
	chartTheme.PaddingLeft = 46
	chartTheme.PaddingRight = 14
	chartTheme.PaddingTop = 30
	chartTheme.PaddingBottom = 30

	now := time.Now()

	dashboard := &dashboard{
		monitor:            monitor,
		chartTheme:         chartTheme,
		start:              now.Add(-chartWindow),
		end:                now,
		logError:           monitor.logError,
		events:             make([]string, 0, 32),
		tracePoints:        make([]series.Point, 0, traceHistorySize),
		traceSeries:        make([]series.XY, 0, traceHistorySize),
		traceRegions:       make([]chart.RegionAnnotation, 0, traceHistorySize),
		applicationSeries:  make([]series.XY, 0, historySize*2),
		applicationRegions: make([]chart.RegionAnnotation, 0, historySize*2),
		connectionSeries:   make([]series.XY, 0, historySize*2),
	}

	for index := range dashboard.applicationPoints {
		dashboard.applicationPoints[index] = make([]series.Point, 0, historySize)
		dashboard.connectionPoints[index] = make([]series.Point, 0, historySize)
	}

	for index := range dashboard.points {
		dashboard.points[index] = make([]series.Point, 0, historySize)
	}

	for index := range dashboard.regions {
		dashboard.regions[index] = make([]chart.RegionAnnotation, 0, historySize)
		dashboard.series[index] = make([]series.XY, 0, historySize*3)
	}

	return dashboard
}

func label(text string, size float32, color gui.Color) gui.View {
	return gui.Text(gui.TextCfg{Text: text, TextStyle: gui.TextStyle{Size: size, Color: color}, Sizing: gui.FitFit})
}

func paragraph(text string, size float32, color gui.Color) gui.View {
	return gui.Text(gui.TextCfg{Text: text, TextStyle: gui.TextStyle{Size: size, Color: color}, Sizing: gui.FillFit, Mode: gui.TextModeWrap})
}

func fixedLabel(text string, width float32, color gui.Color) gui.View {
	return gui.Column(gui.ContainerCfg{Sizing: gui.FixedFit, Width: width, Padding: gui.PaddingNone, Content: []gui.View{label(text, 12, color)}})
}

func metricCard(title, value, detail string, color gui.Color) gui.View {
	if value == "" {
		value = "--"
	}

	return panel([]gui.View{label(title, 10, colorMuted), paragraph(value, 14, color), paragraph(detail, 11, colorMuted)})
}

func panel(content []gui.View) gui.View {
	return gui.Column(gui.ContainerCfg{Sizing: gui.FillFit, Color: colorPanel, ColorBorder: colorBorder, Radius: gui.RadiusPx(0), Padding: gui.PadAll(6), Spacing: gui.SpacingPx(2), Content: content})
}

func probeRow(content []gui.View) gui.View {
	return gui.Row(gui.ContainerCfg{Sizing: gui.FillFit, Padding: gui.PadVH(2, 0), Spacing: gui.SpacingPx(6), Content: content})
}

func probeDetail(result *probeResult) gui.View {
	if result.ICMPAttempted > 0 {
		hosts := make([]gui.View, 0, icmpTargetCount)

		for index := range result.ICMP {
			target := &result.ICMP[index]
			hosts = append(hosts, hostResult(target.Target, target.Status, target.Duration, target.Detail))
		}

		return gui.Row(gui.ContainerCfg{Sizing: gui.FillFit, Padding: gui.PaddingNone, Spacing: gui.SpacingPx(6), Content: hosts})
	}

	if result.Query == "" && result.Status == statusOK {
		return hostResult(result.Target, result.Status, result.Duration, result.Detail)
	}

	if result.Query == "" || result.DNSAttempted == 0 {
		return paragraph(result.Detail, 11, colorMuted)
	}

	hosts := make([]gui.View, 0, dnsTargetCount)

	for index := range result.DNS {
		query := &result.DNS[index]
		hosts = append(hosts, hostResult(query.Query, query.Status, query.Duration, query.Detail))
	}

	return gui.Row(gui.ContainerCfg{Sizing: gui.FillFit, Padding: gui.PaddingNone, Spacing: gui.SpacingPx(6), Content: hosts})
}

func hostResult(host, status string, duration time.Duration, detail string) gui.View {
	value := status

	switch status {
	case statusOK:
		value = strconv.FormatFloat(float64(duration)/float64(time.Millisecond), 'f', 1, 64) + "ms"
	case statusDNSError:
		value = detail
	}

	return paragraph(host+" "+value, 11, statusColor(status))
}

func probeStateChanged(current, previous *probeResult) bool {
	if current.Status != previous.Status {
		return true
	}

	for index := range current.DNS {
		if current.DNS[index].Status != previous.DNS[index].Status {
			return true
		}
	}

	for index := range current.ICMP {
		if current.ICMP[index].Status != previous.ICMP[index].Status {
			return true
		}
	}

	return false
}

func speedText(speed uint64) string {
	if speed == 0 || speed == ^uint64(0) {
		return "Unknown"
	}

	if speed >= 1000000000 {
		return strconv.FormatFloat(float64(speed)/1000000000, 'f', -1, 64) + " Gbps"
	}

	return strconv.FormatFloat(float64(speed)/1000000, 'f', -1, 64) + " Mbps"
}

func statusColor(status string) gui.Color {
	switch status {
	case statusOK:
		return colorGreen
	case statusTimeout, statusPartial, statusIncomplete:
		return colorAmber
	case statusPermission:
		return colorOrange
	case statusError, statusDNSError, statusRefused, statusTLS, statusHTTP:
		return colorRed
	default:
		return colorMuted
	}
}

func neighborColor(state string) gui.Color {
	switch state {
	case "reachable", "permanent", "stale", "delay", "probe", "noarp":
		return colorGreen
	case "unreachable", "missing":
		return colorRed
	default:
		return colorAmber
	}
}

func lineChart(config chart.LineCfg, emptyMessage string) gui.View {
	if len(config.Series) > 0 {
		return chart.Line(config)
	}

	return gui.Column(gui.ContainerCfg{
		ID:      config.ID + "-empty",
		Sizing:  config.Sizing,
		Width:   config.Width,
		Height:  config.Height,
		Color:   colorChart,
		Padding: gui.PadAll(12),
		Spacing: gui.SpacingPx(6),
		Content: []gui.View{label(config.Title, 13, colorText), paragraph(emptyMessage, 11, colorMuted)},
	})
}
