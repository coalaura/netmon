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

var applicationColors = [2]gui.Color{colorTeal, colorSand}

// Called with monitor.mu held. Each completed batch contributes once, not once per UI refresh.
func (dashboard *dashboard) refreshApplications() {
	monitor := dashboard.monitor

	dashboard.applicationSeries = dashboard.applicationSeries[:0]
	dashboard.applicationRegions = dashboard.applicationRegions[:0]

	var values [historySize]time.Duration

	for checkIndex := range applicationNames {
		dashboard.applicationPoints[checkIndex] = dashboard.applicationPoints[checkIndex][:0]

		stats := &dashboard.applicationStats[checkIndex]
		*stats = latencySummary{}

		var (
			segment          int
			previous         *applicationSample
			previousTime     time.Time
			previousDuration time.Duration
		)

		for offset := range monitor.applicationCount {
			index := (monitor.applicationNext - monitor.applicationCount + offset + len(monitor.applications)) % len(monitor.applications)

			current := monitor.applications[index]
			result := &current.Checks[checkIndex]

			if result.Started.Before(dashboard.start) || result.Started.After(dashboard.end) {
				continue
			}

			duration, success := applicationLatency(result, checkIndex)
			boundary := !success

			if previous != nil {
				before := &previous.Checks[checkIndex]
				gap := result.Started.Sub(before.Started)
				boundary = boundary || !sameApplicationNetwork(&previous.Interface, &current.Interface) || before.Target != result.Target || gap <= 0 || gap > applicationInterval+sampleInterval
			}

			if boundary {
				dashboard.appendApplicationSeries(checkIndex, segment)

				segment = len(dashboard.applicationPoints[checkIndex])
				previousTime = time.Time{}
			}

			coordinate := float64(result.Started.UnixNano()) / float64(time.Second)

			if success {
				dashboard.applicationPoints[checkIndex] = append(dashboard.applicationPoints[checkIndex], series.Point{X: coordinate, Y: float64(duration) / float64(time.Millisecond)})
			} else if result.Status != statusCancelled && result.Status != statusUnavailable {
				color := statusColor(result.Status)
				color.A = 38

				width := max(0.5, result.Finished.Sub(result.Started).Seconds())
				dashboard.applicationRegions = append(dashboard.applicationRegions, chart.RegionAnnotation{Axis: chart.AnnotationX, Min: coordinate, Max: coordinate + width, Color: color})
			}

			previous = current
			matches := sameApplicationNetwork(&current.Interface, &dashboard.current.Interface) && dashboard.application != nil && result.Target == dashboard.application.Checks[checkIndex].Target

			if !matches || result.Status == statusCancelled {
				previousTime = time.Time{}

				continue
			}

			attempts, successes := outcomeCounts(result.Status)

			stats.Attempts += attempts
			stats.Failed += attempts - successes

			if !success {
				continue
			}

			values[stats.Samples] = duration

			stats.Samples++

			gap := result.Started.Sub(previousTime)

			if !previousTime.IsZero() && gap > 0 && gap <= applicationInterval+sampleInterval {
				stats.Jitter += (duration - previousDuration).Abs()
				stats.JitterPairs++
			}

			previousTime = result.Started
			previousDuration = duration
		}

		dashboard.appendApplicationSeries(checkIndex, segment)

		stats.finish(values[:])
	}
}

func (dashboard *dashboard) appendApplicationSeries(index, start int) {
	points := dashboard.applicationPoints[index]

	if start < len(points) {
		dashboard.applicationSeries = append(dashboard.applicationSeries, series.NewXY(series.XYCfg{Name: applicationNames[index], Color: applicationColors[index], Points: points[start:]}))
	}
}

func (dashboard *dashboard) applicationChart() gui.View {
	maximum := float64(10)

	for index := range dashboard.applicationPoints {
		for _, point := range dashboard.applicationPoints[index] {
			maximum = max(maximum, point.Y*1.15)
		}
	}

	plot := lineChart(chart.LineCfg{
		ID:            "application",
		Title:         "TCP / HTTPS latency",
		Sizing:        gui.FillFixed,
		Height:        180,
		Theme:         dashboard.chartTheme,
		Version:       dashboard.version,
		TooltipXLabel: "Time",
		TooltipYLabel: "ms",
		Annotations:   chart.Annotations{Regions: dashboard.applicationRegions},
		Series:        dashboard.applicationSeries,
		XAxis:         &fixedTimeAxis{Time: axis.NewTime(axis.TimeCfg{Min: dashboard.start, Max: dashboard.end, Format: "15:04:05"})},
		YAxis:         &fixedValueAxis{Linear: axis.NewLinear(axis.LinearCfg{Title: "ms", Min: 0, Max: maximum, TickCount: 5})},
		LineWidth:     1.5,
		ShowMarkers:   false,
	}, "Waiting for successful TCP / HTTPS checks")

	return gui.Column(gui.ContainerCfg{
		Sizing:  gui.FillFit,
		Padding: gui.PaddingNone,
		Spacing: gui.SpacingPx(2),
		Content: []gui.View{
			gui.Row(gui.ContainerCfg{Padding: gui.PaddingNone, Spacing: gui.SpacingPx(10), Content: []gui.View{label("TCP connect", 11, applicationColors[0]), label("HTTPS total", 11, applicationColors[1]), label("2s checks", 11, colorMuted)}}),
			plot,
		},
	})
}

func (dashboard *dashboard) appendApplicationRows(rows []gui.View) []gui.View {
	targets := [2]string{tcpTarget, "www.gstatic.com"}

	for index, name := range applicationNames {
		status := "WAITING"
		color := colorMuted
		latency := "--"
		detail := "Waiting for a usable IPv4 default route"

		if dashboard.applicationRunning {
			detail = "First check in progress"
		}

		if dashboard.application != nil {
			result := &dashboard.application.Checks[index]

			status = strings.ToUpper(result.Status)
			color = statusColor(result.Status)
			detail = applicationDetail(result, index)
			duration, success := applicationLatency(result, index)

			if success {
				latency = fmt.Sprintf("%.1f ms", float64(duration)/float64(time.Millisecond))
			}

			if dashboard.showStats {
				detail = statisticsText(&dashboard.applicationStats[index]) + " | " + detail
			}

			if !sameApplicationNetwork(&dashboard.application.Interface, &dashboard.current.Interface) {
				status = "STALE"
				color = colorMuted
				detail = "Previous network | " + detail
			}

			if dashboard.applicationRunning {
				detail += " | checking..."
			}
		}

		rows = append(rows, probeRow([]gui.View{
			fixedLabel(name, 110, applicationColors[index]),
			fixedLabel(status, 94, color),
			fixedLabel(latency, 84, colorText),
			fixedLabel(failureText(&dashboard.applicationStats[index]), 72, colorMuted),
			fixedLabel(targets[index], 108, colorText),
			paragraph(detail, 11, color),
		}))
	}

	return rows
}

func applicationLatency(result *applicationResult, index int) (time.Duration, bool) {
	if result.Status != statusOK {
		return 0, false
	}

	if index == 0 {
		return result.TCP.Duration, result.TCP.Measured
	}

	return result.Finished.Sub(result.Started), !result.Started.IsZero() && !result.Finished.Before(result.Started)
}

func applicationDetail(result *applicationResult, index int) string {
	detail := "TCP " + phaseText(result.TCP)

	if index == 1 {
		detail = "DNS " + phaseText(result.DNS) + " | " + detail
	}

	if index == 1 {
		detail += " | TLS " + phaseText(result.TLS) + " | TTFB " + phaseText(result.TTFB)

		if result.HTTPStatus != 0 {
			detail += fmt.Sprintf(" | HTTP %d", result.HTTPStatus)
		}
	}

	if result.Status != statusOK {
		detail += " | " + result.Stage + ": " + result.Detail
	}

	return detail
}
