package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-gui-org/go-charts/axis"
	"github.com/go-gui-org/go-charts/chart"
	"github.com/go-gui-org/go-charts/series"
	"github.com/go-gui-org/go-gui/gui"
)

type fixedHopAxis struct {
	*axis.Linear
}

func (*fixedHopAxis) SetRange(_, _ float64) {}

// Called while refresh holds the monitor's read lock. Completed traces are immutable.
func (dashboard *dashboard) refreshTraces() {
	monitor := dashboard.monitor

	dashboard.traceRunning = monitor.traceRunning
	dashboard.traceDue = monitor.traceDue
	dashboard.tracePoints = dashboard.tracePoints[:0]
	dashboard.traceSeries = dashboard.traceSeries[:0]
	dashboard.traceRegions = dashboard.traceRegions[:0]

	start := dashboard.end.Add(-traceWindow)

	var (
		segment  int
		previous *traceResult
	)

	for offset := range monitor.traceCount {
		index := (monitor.traceNext - monitor.traceCount + offset + traceHistorySize) % traceHistorySize

		result := monitor.traces[index]
		if result.Finished.Before(start) {
			continue
		}

		boundary := result.Status != statusOK

		if previous != nil {
			boundary = boundary || !sameTraceNetwork(&previous.Interface, &result.Interface) || result.Finished.Sub(previous.Finished) > 2*traceInterval
		}

		if boundary {
			dashboard.appendTraceSeries(segment)

			segment = len(dashboard.tracePoints)
		}

		coordinate := float64(result.Finished.UnixNano()) / float64(time.Second)

		if result.Status == statusOK {
			dashboard.tracePoints = append(dashboard.tracePoints, series.Point{X: coordinate, Y: float64(result.HopCount)})
		} else {
			color := statusColor(result.Status)
			color.A = 55

			dashboard.traceRegions = append(dashboard.traceRegions, chart.RegionAnnotation{Axis: chart.AnnotationX, Min: coordinate - 10, Max: coordinate, Color: color})
		}

		previous = result
	}

	dashboard.appendTraceSeries(segment)

	if monitor.traceCount == 0 {
		return
	}

	latest := monitor.traces[(monitor.traceNext+traceHistorySize-1)%traceHistorySize]
	if latest != dashboard.trace {
		message := "Traceroute: " + latest.Status + " | " + latest.Detail

		if latest.Change != "" {
			message += " | " + latest.Change
		}

		dashboard.addEvent(message)
		dashboard.trace = latest
	}
}

func (dashboard *dashboard) appendTraceSeries(start int) {
	if start < len(dashboard.tracePoints) {
		dashboard.traceSeries = append(dashboard.traceSeries, series.NewXY(series.XYCfg{Name: "Destination hop count", Color: colorSand, Points: dashboard.tracePoints[start:]}))
	}
}

func (dashboard *dashboard) tracePanel() gui.View {
	schedule := "waiting for an IPv4 default route"

	if dashboard.traceRunning {
		schedule = "tracing..."
	} else if dashboard.current.Interface.Link == "up" && dashboard.current.Interface.DefaultRoute && !dashboard.traceDue.IsZero() {
		schedule = "next " + dashboard.traceDue.Format("15:04:05")
	}

	rows := make([]gui.View, 0, 5)
	rows = append(rows, paragraph("TRACEROUTE  "+ICMPRemoteTargets[0]+"  |  "+traceProtocol+"  |  every "+traceInterval.String()+"  |  "+schedule, 11, colorText))

	result := dashboard.trace
	if result == nil {
		rows = append(rows, paragraph("Waiting for the first trace. Up to 30 hops, 800ms per hop; monitoring continues independently.", 11, colorMuted))
	} else {
		summary := fmt.Sprintf("%s  %s  |  %s  |  %s", result.Finished.Format("15:04:05"), strings.ToUpper(result.Status), result.Interface.Name, result.Detail)

		if result.Status == statusOK {
			summary = fmt.Sprintf("%s  %d HOPS  |  destination %.1fms  |  %s", result.Finished.Format("15:04:05"), result.HopCount, float64(result.Hops[result.Count-1].Duration)/float64(time.Millisecond), result.Interface.Name)
		}

		if !sameTraceNetwork(&result.Interface, &dashboard.current.Interface) {
			summary = "PREVIOUS NETWORK  |  " + summary
		}

		rows = append(rows, paragraph(summary, 12, statusColor(result.Status)))

		if result.Change != "" {
			rows = append(rows, paragraph(result.Change, 11, colorAmber))
		}

		rows = append(rows, paragraph(tracePath(result), 11, colorMuted))
	}

	rows = append(rows, paragraph("* no reply; incomplete traces have no hop count. RTT is source-to-hop, not link cost.", 10, colorMuted))

	plot := lineChart(chart.LineCfg{
		ID:            "traceroute",
		Title:         "Destination hops / 60 min",
		Sizing:        gui.FixedFixed,
		Width:         340,
		Height:        130,
		Theme:         dashboard.chartTheme,
		Version:       dashboard.version,
		TooltipXLabel: "Time",
		TooltipYLabel: "hops",
		Series:        dashboard.traceSeries,
		Annotations:   chart.Annotations{Regions: dashboard.traceRegions},
		XAxis:         &fixedTimeAxis{Time: axis.NewTime(axis.TimeCfg{Min: dashboard.end.Add(-traceWindow), Max: dashboard.end, Format: "15:04"})},
		YAxis:         &fixedHopAxis{Linear: axis.NewLinear(axis.LinearCfg{Min: 0, Max: traceMaxHops, TickCount: 4})},
		LineWidth:     1.5,
		ShowMarkers:   false,
	}, "No completed trace in this window")

	return panel([]gui.View{gui.Row(gui.ContainerCfg{
		Sizing:  gui.FillFit,
		Padding: gui.PaddingNone,
		Spacing: gui.SpacingPx(6),
		Content: []gui.View{plot, gui.Column(gui.ContainerCfg{Sizing: gui.FillFit, Padding: gui.PaddingNone, Spacing: gui.SpacingPx(3), Content: rows})},
	})})
}

func tracePath(result *traceResult) string {
	var text strings.Builder

	text.Grow(result.Count * 32)

	for index := range result.Count {
		hop := &result.Hops[index]

		if index > 0 {
			text.WriteString("  >  ")
		}

		if hop.Address.IsValid() {
			fmt.Fprintf(&text, "%d:%s %.1fms", hop.TTL, hop.Address, float64(hop.Duration)/float64(time.Millisecond))
		} else {
			fmt.Fprintf(&text, "%d:*", hop.TTL)
		}

		if hop.Status != statusOK && hop.Status != statusTimeout {
			fmt.Fprintf(&text, " [%s]", hop.Status)
		}
	}

	return text.String()
}
