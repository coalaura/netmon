package main

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/go-gui-org/go-charts/axis"
	"github.com/go-gui-org/go-charts/chart"
	"github.com/go-gui-org/go-charts/series"
	"github.com/go-gui-org/go-gui/gui"
)

var (
	connectionNames  = [3]string{"Active", "Established", "TIME_WAIT"}
	connectionColors = [3]gui.Color{colorTeal, colorSand, colorMuted}
)

// Counts are independent of the selected interface and stay continuous across route changes.
func (dashboard *dashboard) refreshConnections() {
	monitor := dashboard.monitor
	dashboard.connectionSeries = dashboard.connectionSeries[:0]

	for index := range dashboard.connectionPoints {
		dashboard.connectionPoints[index] = dashboard.connectionPoints[index][:0]
	}

	var (
		segment  int
		previous time.Time
	)

	for offset := range monitor.count {
		index := (monitor.next - monitor.count + offset + historySize) % historySize

		current := &monitor.history[index]
		if current.Time.Before(dashboard.start) {
			continue
		}

		gap := current.Time.Sub(previous)
		if !current.Connections.Available || !previous.IsZero() && (gap <= 0 || gap > 1500*time.Millisecond) {
			dashboard.appendConnectionSeries(segment)
			segment = len(dashboard.connectionPoints[0])
		}

		if current.Connections.Available {
			coordinate := float64(current.Time.UnixNano()) / float64(time.Second)

			dashboard.connectionPoints[0] = append(dashboard.connectionPoints[0], series.Point{X: coordinate, Y: float64(current.Connections.active())})
			dashboard.connectionPoints[1] = append(dashboard.connectionPoints[1], series.Point{X: coordinate, Y: float64(current.Connections.Established)})
			dashboard.connectionPoints[2] = append(dashboard.connectionPoints[2], series.Point{X: coordinate, Y: float64(current.Connections.TimeWait)})
		}

		previous = current.Time
	}

	dashboard.appendConnectionSeries(segment)
}

func (dashboard *dashboard) appendConnectionSeries(start int) {
	for index := range dashboard.connectionPoints {
		points := dashboard.connectionPoints[index]

		if start < len(points) {
			dashboard.connectionSeries = append(dashboard.connectionSeries, series.NewXY(series.XYCfg{Name: connectionNames[index], Color: connectionColors[index], Points: points[start:]}))
		}
	}
}

func (dashboard *dashboard) connectionChart() gui.View {
	maximum := float64(10)

	for _, points := range dashboard.connectionPoints {
		for _, point := range points {
			maximum = max(maximum, math.Ceil(point.Y*1.15/5)*5)
		}
	}

	current := &dashboard.current.Connections

	active := "Active --"
	established := "Established --"
	timeWait := "TIME_WAIT --"

	if current.Available {
		active = fmt.Sprintf("Active %d", current.active())
		established = fmt.Sprintf("Established %d", current.Established)
		timeWait = fmt.Sprintf("TIME_WAIT %d", current.TimeWait)
	}

	plot := lineChart(chart.LineCfg{
		ID:            "connections",
		Title:         "TCP connections / system-wide",
		Sizing:        gui.FillFixed,
		Height:        180,
		Theme:         dashboard.chartTheme,
		Version:       dashboard.version,
		TooltipXLabel: "Time",
		TooltipYLabel: "Connections",
		Series:        dashboard.connectionSeries,
		XAxis:         &fixedTimeAxis{Time: axis.NewTime(axis.TimeCfg{Min: dashboard.start, Max: dashboard.end, Format: "15:04:05"})},
		YAxis:         &fixedValueAxis{Linear: axis.NewLinear(axis.LinearCfg{Min: 0, Max: maximum, TickCount: 6, TickFormat: connectionTick})},
		LineWidth:     1.5,
		ShowMarkers:   false,
	}, "Waiting for TCP connection counts")

	content := []gui.View{
		gui.Row(gui.ContainerCfg{Padding: gui.PaddingNone, Spacing: gui.SpacingPx(10), Content: []gui.View{label(active, 11, connectionColors[0]), label(established, 11, connectionColors[1]), label(timeWait, 11, connectionColors[2]), label("1s snapshots", 11, colorMuted)}}),
		plot,
	}

	if !current.Available && current.Detail != "" {
		content = append(content, paragraph(current.Detail, 11, colorAmber))
	}

	return gui.Column(gui.ContainerCfg{Sizing: gui.FillFit, Padding: gui.PaddingNone, Spacing: gui.SpacingPx(2), Content: content})
}

func connectionTick(value float64) string {
	return strconv.FormatFloat(value, 'f', 0, 64)
}
