// Command icon generates Netmon's artwork and Windows icon resources.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"strings"
)

const (
	logoSize = 64
	samples  = 4
)

type point struct {
	x float64
	y float64
}

var (
	route      = [...]point{{18, 44}, {18, 20}, {46, 44}, {46, 20}}
	background = color.RGBA{R: 0x1B, G: 0x1B, B: 0x1B, A: 255}
	lineColor  = color.RGBA{R: 0x76, G: 0xD6, B: 0xA1, A: 255}
	nodeColor  = color.RGBA{R: 0x45, G: 0xCD, B: 0xBF, A: 255}
)

func main() {
	err := generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate() error {
	err := os.WriteFile("assets/logo.svg", []byte(svg()), 0o644)
	if err != nil {
		return err
	}

	sizes := [...]int{16, 20, 24, 32, 40, 48, 64, 128, 256}
	images := make([][]byte, 0, len(sizes))

	var encoded bytes.Buffer

	for _, size := range sizes {
		encoded.Reset()

		err = png.Encode(&encoded, raster(size))
		if err != nil {
			return err
		}

		images = append(images, bytes.Clone(encoded.Bytes()))
	}

	err = os.WriteFile("assets/logo.png", images[len(images)-1], 0o644)
	if err != nil {
		return err
	}

	// ICO entries contain independently rasterized PNGs, avoiding blurry small icons.
	offset := 6 + 16*len(sizes)
	icon := make([]byte, offset, offset+64*1024)
	binary.LittleEndian.PutUint16(icon[2:], 1)
	binary.LittleEndian.PutUint16(icon[4:], uint16(len(sizes)))

	for index, size := range sizes {
		entry := icon[6+16*index:]
		entry[0] = byte(size) // ICO uses zero for 256 pixels.
		entry[1] = byte(size)
		binary.LittleEndian.PutUint16(entry[4:], 1)
		binary.LittleEndian.PutUint16(entry[6:], 32)
		binary.LittleEndian.PutUint32(entry[8:], uint32(len(images[index])))
		binary.LittleEndian.PutUint32(entry[12:], uint32(offset))
		offset += len(images[index])
	}

	for _, data := range images {
		icon = append(icon, data...)
	}

	err = os.WriteFile("assets/logo.ico", icon, 0o644)
	if err != nil {
		return err
	}

	architectures := [...]string{"amd64", "arm64"}
	targets := [...]string{"x64", "arm64"}

	for index, architecture := range architectures {
		command := exec.Command("zig", "rc", "/:output-format", "coff", "/:target", targets[index], "/fo", "../logo_windows_"+architecture+".syso", "logo.rc")
		command.Dir = "assets"
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr

		err = command.Run()
		if err != nil {
			return fmt.Errorf("compile %s icon resource: %w", architecture, err)
		}
	}

	return nil
}

func svg() string {
	var result strings.Builder

	result.Grow(640)
	result.WriteString("<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 64 64\">\n\t<title>Netmon</title>\n")
	fmt.Fprintf(&result, "\t<rect x=\"4\" y=\"4\" width=\"56\" height=\"56\" rx=\"14\" fill=\"#%02x%02x%02x\"/>\n", background.R, background.G, background.B)
	result.WriteString("\t<path d=\"M")

	for index, position := range route {
		if index != 0 {
			result.WriteString(" L")
		}

		fmt.Fprintf(&result, "%g %g", position.x, position.y)
	}

	fmt.Fprintf(&result, "\" fill=\"none\" stroke=\"#%02x%02x%02x\" stroke-width=\"5\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/>\n", lineColor.R, lineColor.G, lineColor.B)

	for index := 0; index < len(route); index += len(route) - 1 {
		position := route[index]
		fmt.Fprintf(&result, "\t<circle cx=\"%g\" cy=\"%g\" r=\"4.5\" fill=\"#%02x%02x%02x\"/>\n", position.x, position.y, nodeColor.R, nodeColor.G, nodeColor.B)
	}

	result.WriteString("</svg>\n")

	return result.String()
}

func raster(size int) *image.RGBA {
	result := image.NewRGBA(image.Rect(0, 0, size, size))
	scale := float64(logoSize) / float64(size*samples)

	// Average premultiplied samples so transparent edges stay clean at small sizes.
	for row := range size {
		for column := range size {
			var (
				red   uint32
				green uint32
				blue  uint32
				alpha uint32
			)

			for sampleY := range samples {
				for sampleX := range samples {
					x := (float64(column*samples+sampleX) + 0.5) * scale
					y := (float64(row*samples+sampleY) + 0.5) * scale
					pixel := pixelAt(x, y)
					red += uint32(pixel.R)
					green += uint32(pixel.G)
					blue += uint32(pixel.B)
					alpha += uint32(pixel.A)
				}
			}

			result.SetRGBA(column, row, color.RGBA{R: uint8(red / (samples * samples)), G: uint8(green / (samples * samples)), B: uint8(blue / (samples * samples)), A: uint8(alpha / (samples * samples))})
		}
	}

	return result
}

func pixelAt(x, y float64) color.RGBA {
	// Distance to the inner square gives the tile's rounded corners.
	deltaX := math.Max(math.Abs(x-32)-14, 0)
	deltaY := math.Max(math.Abs(y-32)-14, 0)

	if deltaX*deltaX+deltaY*deltaY > 14*14 {
		return color.RGBA{}
	}

	for index := 0; index < len(route); index += len(route) - 1 {
		deltaX = x - route[index].x
		deltaY = y - route[index].y

		if deltaX*deltaX+deltaY*deltaY <= 4.5*4.5 {
			return nodeColor
		}
	}

	for index := 1; index < len(route); index++ {
		start := route[index-1]
		end := route[index]
		deltaX = end.x - start.x
		deltaY = end.y - start.y
		projection := ((x-start.x)*deltaX + (y-start.y)*deltaY) / (deltaX*deltaX + deltaY*deltaY)
		projection = math.Max(0, math.Min(1, projection))

		distanceX := x - start.x - projection*deltaX
		distanceY := y - start.y - projection*deltaY

		if distanceX*distanceX+distanceY*distanceY <= 2.5*2.5 {
			return lineColor
		}
	}

	return background
}
