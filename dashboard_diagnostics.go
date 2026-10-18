package main

import (
	"fmt"

	"github.com/go-gui-org/go-gui/gui"
)

func (dashboard *dashboard) setFrozen(frozen bool) {
	dashboard.frozen = frozen

	if !frozen {
		dashboard.refresh()
	}
}

func (dashboard *dashboard) toggleFreeze(event gui.EventCtx) {
	dashboard.setFrozen(!dashboard.frozen)

	event.Window.InvalidateLayout()
}

func (dashboard *dashboard) toggleStats(event gui.EventCtx) {
	dashboard.showStats = !dashboard.showStats

	event.Window.InvalidateLayout()
}

func (dashboard *dashboard) trafficRow() gui.View {
	traffic := &dashboard.current.Traffic

	text := "TRAFFIC  |  " + traffic.Detail
	color := colorMuted

	if traffic.Valid {
		text = fmt.Sprintf("TRAFFIC  |  RX %s  /  TX %s  |  Errors RX/TX +%d / +%d  |  Discards RX/TX +%d / +%d  |  %.2fs delta", bitRateText(traffic.RXBitsPerSecond), bitRateText(traffic.TXBitsPerSecond), traffic.RXErrors, traffic.TXErrors, traffic.RXDiscards, traffic.TXDiscards, traffic.Interval.Seconds())

		if traffic.RXErrors > 0 || traffic.TXErrors > 0 {
			color = colorAmber
		}
	}

	return paragraph(text, 11, color)
}

func controlButton(id, text string, action func(gui.EventCtx)) gui.View {
	return gui.Button(gui.ButtonCfg{
		ID:      id,
		Sizing:  gui.FitFit,
		Padding: gui.PadVH(4, 8),
		Radius:  gui.RadiusPx(0),
		Colors:  gui.ColorSet{Base: colorControl, Hover: colorHover, Click: colorBackground, Focus: colorHover, BorderFocus: colorMuted, Border: colorBorder},
		Content: []gui.View{label(text, 11, colorText)},
		OnClick: action,
	})
}

func bitRateText(rate float64) string {
	if rate >= 1e9 {
		return fmt.Sprintf("%.2f Gbps", rate/1e9)
	}

	if rate >= 1e6 {
		return fmt.Sprintf("%.2f Mbps", rate/1e6)
	}

	if rate >= 1e3 {
		return fmt.Sprintf("%.1f Kbps", rate/1e3)
	}

	return fmt.Sprintf("%.0f bps", rate)
}

func failureText(stats *latencySummary) string {
	if stats.Attempts == 0 {
		return "--"
	}

	return fmt.Sprintf("%.1f%%", float64(stats.Failed)*100/float64(stats.Attempts))
}
