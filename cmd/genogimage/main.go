// Command genogimage produces web/static/og.png — the 1200x630 OpenGraph
// image referenced from web/templates/index.html. Pure stdlib so it runs
// during normal builds without adding module deps. Run via `make og-image`.
//
// Design: dark slate canvas (matches bg-slate-950) with two radial auroras
// (crimson top-left, indigo bottom-right), a center "pixel grid" motif
// inspired by the 1x1 tracking pixel, and a red/white/indigo stripe along
// the bottom. Text lives in og:title / og:description, not in the image —
// keeps the asset crisp at any rescale and avoids font dependencies.
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
)

const (
	width  = 1200
	height = 630
)

var (
	bg     = color.RGBA{0x02, 0x06, 0x17, 0xFF} // slate-950
	red    = color.RGBA{0xDC, 0x26, 0x26, 0xFF} // red-600
	white  = color.RGBA{0xF8, 0xFA, 0xFC, 0xFF} // slate-50
	indigo = color.RGBA{0x43, 0x38, 0xCA, 0xFF} // indigo-700
	rose   = color.RGBA{0xE1, 0x1D, 0x48, 0xFF} // rose-600
)

func main() {
	var out string
	flag.StringVar(&out, "out", "web/static/og.png", "output path")
	flag.Parse()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fill(img, bg)
	aurora(img, 180, 130, 520, red, 0.55)
	aurora(img, 1020, 520, 600, indigo, 0.65)
	aurora(img, 600, 315, 320, rose, 0.18)
	pixelGrid(img)
	stripeBar(img, height-12)

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(out)
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatalf("encode: %v", err)
	}
	log.Printf("wrote %s (%dx%d)", out, width, height)
}

// fill paints the entire image a single color.
func fill(img *image.RGBA, c color.RGBA) {
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

// aurora blends a radial gradient of c into img centered at (cx,cy) with
// the given radius and peak alpha. Beyond radius the contribution is zero,
// at the center it equals peak * 255.
func aurora(img *image.RGBA, cx, cy, radius int, c color.RGBA, peak float64) {
	r2 := float64(radius * radius)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			dx, dy := float64(x-cx), float64(y-cy)
			d2 := dx*dx + dy*dy
			if d2 >= r2 {
				continue
			}
			// Smoothstep falloff so the edge is feathered, not a hard disc.
			t := 1 - d2/r2
			a := peak * t * t
			img.SetRGBA(x, y, blend(img.RGBAAt(x, y), c, a))
		}
	}
}

// pixelGrid draws a centered grid of small squares — most dim, a few lit
// in red/white/indigo — evoking the 1x1 tracking pixel concept.
func pixelGrid(img *image.RGBA) {
	const (
		cellSize = 18
		gap      = 6
		cols     = 22
		rows     = 9
	)
	gridW := cols*cellSize + (cols-1)*gap
	gridH := rows*cellSize + (rows-1)*gap
	x0 := (width - gridW) / 2
	y0 := (height - gridH) / 2

	rng := rand.New(rand.NewPCG(0xC0FFEE, 0xBADF00D))
	palette := []color.RGBA{red, white, indigo, rose}

	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			x := x0 + c*(cellSize+gap)
			y := y0 + r*(cellSize+gap)
			// Distance from grid center, normalized [0,1].
			ndx := float64(c-cols/2) / float64(cols/2)
			ndy := float64(r-rows/2) / float64(rows/2)
			dist := math.Sqrt(ndx*ndx + ndy*ndy)
			lit := rng.Float64() < (0.55 - 0.4*dist)
			cell := color.RGBA{0x1E, 0x29, 0x3B, 0xFF} // slate-800
			if lit {
				cell = palette[rng.IntN(len(palette))]
			}
			drawSquare(img, x, y, cellSize, cell)
		}
	}
}

// drawSquare paints a filled cellSize x cellSize square at (x,y).
func drawSquare(img *image.RGBA, x, y, size int, c color.RGBA) {
	for dy := 0; dy < size; dy++ {
		for dx := 0; dx < size; dx++ {
			img.SetRGBA(x+dx, y+dy, c)
		}
	}
}

// stripeBar paints a 12px red/white/indigo stripe across the bottom of img.
func stripeBar(img *image.RGBA, y int) {
	thirds := []color.RGBA{red, white, indigo}
	tw := width / 3
	for i, c := range thirds {
		x0 := i * tw
		x1 := x0 + tw
		if i == len(thirds)-1 {
			x1 = width
		}
		for yy := y; yy < height; yy++ {
			for xx := x0; xx < x1; xx++ {
				img.SetRGBA(xx, yy, c)
			}
		}
	}
}

// blend returns dst painted with src at the given alpha (0..1).
func blend(dst, src color.RGBA, a float64) color.RGBA {
	if a <= 0 {
		return dst
	}
	if a > 1 {
		a = 1
	}
	mix := func(d, s uint8) uint8 { return uint8(float64(d)*(1-a) + float64(s)*a) }
	return color.RGBA{mix(dst.R, src.R), mix(dst.G, src.G), mix(dst.B, src.B), 0xFF}
}
