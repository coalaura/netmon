package main

import (
	"github.com/coalaura/plain"
	"github.com/go-gui-org/go-charts/chart"
	"github.com/go-gui-org/go-charts/series"
	"github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend"
)

var log = plain.New(plain.WithDate(plain.RFC3339Local))

func main() {
	gui.SetTheme(gui.ThemeDark)
	w := gui.NewWindow(gui.WindowCfg{
		Title:  "Line Chart",
		Width:  800,
		Height: 600,
		OnInit: func(w *gui.Window) { w.SetView(view) },
	})
	backend.Run(w)
}

func view(w *gui.Window) gui.View {
	return chart.Line(chart.LineCfg{
		ID: "demo",
		Series: []series.XY{
			series.NewXY(series.XYCfg{
				Name:  "Sales",
				Color: gui.Hex(0x4E79A7),
				Points: []series.Point{
					{X: 1, Y: 10}, {X: 2, Y: 25},
					{X: 3, Y: 18}, {X: 4, Y: 32},
				},
			}),
		},
	})
}
