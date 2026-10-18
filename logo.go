package main

import _ "embed"

//go:generate go run ./tools/icon

var (
	//go:embed assets/logo.png
	logoPNG []byte

	//go:embed assets/logo.svg
	logoSVG string
)
