package cards

import (
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"

	"github.com/fogleman/gg"
)

// ticket cuts the card to a plane ticket: rounded corners and a half-circle
// notch at the top and bottom of the perforation. Everything outside is fully
// transparent; edges are anti-aliased from a signed distance.
func ticket(src image.Image) *image.NRGBA {
	b := src.Bounds()
	out := image.NewNRGBA(b)
	w, h := float64(b.Dx()), float64(b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			a := ticketCoverage(float64(x-b.Min.X)+0.5, float64(y-b.Min.Y)+0.5, w, h)
			if a <= 0 {
				continue
			}
			c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
			c.A = uint8(float64(c.A)*a + 0.5)
			out.SetNRGBA(x, y, c)
		}
	}
	return out
}

// ticketCoverage is how much of the pixel centred on (px, py) is ticket, 0 to 1.
func ticketCoverage(px, py, w, h float64) float64 {
	// Rounded rectangle: distance outside the shape, negative inside.
	qx := math.Abs(px-w/2) - (w/2 - cornerR)
	qy := math.Abs(py-h/2) - (h/2 - cornerR)
	outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - cornerR
	a := clamp01(0.5 - outside)
	// Notches: distance from each notch's edge, positive outside the notch.
	for _, ny := range []float64{0, h} {
		d := math.Hypot(px-mainW, py-ny) - notchR
		a *= clamp01(0.5 + d)
	}
	return a
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// githubMark is GitHub's mark in a 16x16 box (the octicon "mark-github").
const githubMark = "M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"

// drawGitHubMark fills the GitHub mark into a size x size box at x, y.
func drawGitHubMark(dc *gg.Context, x, y, size float64, hex string) {
	dc.Push()
	dc.Translate(x, y)
	dc.Scale(size/16, size/16)
	svgPath(dc, githubMark)
	setHex(dc, hex)
	dc.Fill()
	dc.Pop()
}

// svgPath traces an SVG path's M, L, H, V, C, S, A and Z commands (absolute
// and relative): enough for the marks drawn on cards.
func svgPath(dc *gg.Context, d string) {
	nums := pathTokens(d)
	var cx, cy, sx, sy, lcx, lcy float64 // current point, subpath start, last control
	var cmd byte
	i := 0
	next := func() float64 { v := nums[i].v; i++; return v }
	for i < len(nums) {
		if nums[i].cmd != 0 {
			cmd = nums[i].cmd
			i++
			if cmd == 'z' || cmd == 'Z' {
				dc.ClosePath()
				cx, cy = sx, sy
				lcx, lcy = cx, cy
				continue
			}
		}
		rel := cmd >= 'a'
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = cx, cy
		}
		switch cmd | 0x20 {
		case 'm':
			cx, cy = ox+next(), oy+next()
			sx, sy = cx, cy
			dc.MoveTo(cx, cy)
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'l':
			cx, cy = ox+next(), oy+next()
			dc.LineTo(cx, cy)
		case 'h':
			cx = ox + next()
			dc.LineTo(cx, cy)
		case 'v':
			cy = oy + next()
			dc.LineTo(cx, cy)
		case 'c':
			x1, y1, x2, y2 := ox+next(), oy+next(), ox+next(), oy+next()
			cx, cy = ox+next(), oy+next()
			dc.CubicTo(x1, y1, x2, y2, cx, cy)
			lcx, lcy = x2, y2
			continue
		case 's':
			x1, y1 := 2*cx-lcx, 2*cy-lcy
			x2, y2 := ox+next(), oy+next()
			cx, cy = ox+next(), oy+next()
			dc.CubicTo(x1, y1, x2, y2, cx, cy)
			lcx, lcy = x2, y2
			continue
		case 'a':
			rx, ry, rot := next(), next(), next()
			large, sweep := next() != 0, next() != 0
			ex, ey := ox+next(), oy+next()
			arcTo(dc, cx, cy, rx, ry, rot, large, sweep, ex, ey)
			cx, cy = ex, ey
		default:
			return
		}
		lcx, lcy = cx, cy
	}
}

type pathToken struct {
	cmd byte
	v   float64
}

// pathTokens splits path data into commands and numbers. Arc flags are single
// digits that may run into the next number ("0016" is 0, 0, 16).
func pathTokens(d string) []pathToken {
	var out []pathToken
	var arcArg, lastCmd = 0, byte(0)
	for i := 0; i < len(d); {
		c := d[i]
		switch {
		case strings.IndexByte("MmLlHhVvCcSsAaZz", c) >= 0:
			out = append(out, pathToken{cmd: c})
			lastCmd, arcArg = c|0x20, 0
			i++
		case c == ' ' || c == ',' || c == '\n' || c == '\t':
			i++
		default:
			if lastCmd == 'a' && (arcArg%7 == 3 || arcArg%7 == 4) && (c == '0' || c == '1') {
				out = append(out, pathToken{v: float64(c - '0')})
				arcArg++
				i++
				continue
			}
			j := i
			if d[j] == '-' || d[j] == '+' {
				j++
			}
			dot := false
			for j < len(d) && (d[j] >= '0' && d[j] <= '9' || d[j] == '.' && !dot) {
				if d[j] == '.' {
					dot = true
				}
				j++
			}
			if j == i {
				i++ // stray byte
				continue
			}
			v, _ := strconv.ParseFloat(d[i:j], 64)
			out = append(out, pathToken{v: v})
			arcArg++
			i = j
		}
	}
	return out
}

// arcTo draws an SVG endpoint arc as gg arc segments (SVG 1.1 appendix F.6).
func arcTo(dc *gg.Context, x1, y1, rx, ry, rot float64, large, sweep bool, x2, y2 float64) {
	if rx == 0 || ry == 0 {
		dc.LineTo(x2, y2)
		return
	}
	phi := rot * math.Pi / 180
	cp, sp := math.Cos(phi), math.Sin(phi)
	dx, dy := (x1-x2)/2, (y1-y2)/2
	x1p, y1p := cp*dx+sp*dy, -sp*dx+cp*dy
	rx, ry = math.Abs(rx), math.Abs(ry)
	if l := x1p*x1p/(rx*rx) + y1p*y1p/(ry*ry); l > 1 {
		rx, ry = rx*math.Sqrt(l), ry*math.Sqrt(l)
	}
	num := rx*rx*ry*ry - rx*rx*y1p*y1p - ry*ry*x1p*x1p
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	co := math.Sqrt(math.Max(0, num/den))
	if large == sweep {
		co = -co
	}
	cxp, cyp := co*rx*y1p/ry, -co*ry*x1p/rx
	ccx := cp*cxp - sp*cyp + (x1+x2)/2
	ccy := sp*cxp + cp*cyp + (y1+y2)/2
	ang := func(ux, uy, vx, vy float64) float64 { return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy) }
	t1 := ang(1, 0, (x1p-cxp)/rx, (y1p-cyp)/ry)
	dt := ang((x1p-cxp)/rx, (y1p-cyp)/ry, (-x1p-cxp)/rx, (-y1p-cyp)/ry)
	if !sweep && dt > 0 {
		dt -= 2 * math.Pi
	} else if sweep && dt < 0 {
		dt += 2 * math.Pi
	}
	const steps = 24
	for k := 1; k <= steps; k++ {
		t := t1 + dt*float64(k)/steps
		ex, ey := rx*math.Cos(t), ry*math.Sin(t)
		dc.LineTo(cp*ex-sp*ey+ccx, sp*ex+cp*ey+ccy)
	}
}
