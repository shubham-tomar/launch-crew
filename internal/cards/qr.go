package cards

import (
	"fmt"
	"image"
	"math"

	"github.com/fogleman/gg"
	qrcode "github.com/skip2/go-qrcode"
)

const (
	slideW, slideH = 1920, 1080
	qrSide         = 432
	// qrMark is the centre tile. At 96px it covers about 5% of the code, well
	// inside what error correction Highest (30%) recovers.
	qrMark       = 96
	qrMarkRadius = 22
	qrMarkInset  = 18 // the plane's box sits this far inside the tile on each side
	qrLogoBox    = 72 // the custom image is scaled to fit this square
)

// QR centre marks.
const (
	LogoPlane  = "plane"
	LogoNone   = "none"
	LogoCustom = "custom"
)

// SlideLogo says what goes over the centre of the slide's QR code. The zero
// value is the plane. Custom is an image fetched from URL; if that cannot be
// loaded the plane is drawn instead, so the slide always renders.
type SlideLogo struct {
	Mode string
	URL  string
	// Image, when set, is the custom mark already fetched; it wins over URL.
	Image image.Image
}

// planePath is the Launch Crew plane in a 24x24 box, nose up: a polygon with
// two cubic curves at the nose (the same shape as the mark in the web app).
func planePath(dc *gg.Context) {
	dc.MoveTo(12, 2.2)
	dc.CubicTo(13.05, 2.2, 13.85, 3.5, 13.85, 5.2)
	dc.LineTo(13.85, 9.3)
	dc.LineTo(20.85, 13.2)
	dc.LineTo(20.85, 15.3)
	dc.LineTo(13.85, 13.3)
	dc.LineTo(13.85, 16.8)
	dc.LineTo(16.2, 18.65)
	dc.LineTo(16.2, 20.2)
	dc.LineTo(12, 19.1)
	dc.LineTo(7.8, 20.3)
	dc.LineTo(7.8, 18.75)
	dc.LineTo(10.15, 16.9)
	dc.LineTo(10.15, 13.4)
	dc.LineTo(3.15, 15.4)
	dc.LineTo(3.15, 13.3)
	dc.LineTo(10.15, 9.4)
	dc.LineTo(10.15, 5.2)
	dc.CubicTo(10.15, 3.5, 10.95, 2.2, 12, 2.2)
	dc.ClosePath()
}

// drawPlane fills the plane, rotated deg degrees, into a size x size box centred on cx, cy.
func drawPlane(dc *gg.Context, cx, cy, size, deg float64, hex string) {
	dc.Push()
	dc.Translate(cx, cy)
	dc.Rotate(deg * math.Pi / 180)
	dc.Scale(size/24, size/24)
	dc.Translate(-12, -12)
	planePath(dc)
	setHex(dc, hex)
	dc.Fill()
	dc.Pop()
}

// drawQRMark covers the centre of the code with a white rounded tile holding the mark.
func drawQRMark(dc *gg.Context, logo SlideLogo) {
	if logo.Mode == LogoNone {
		return
	}
	cx, cy := float64(slideW)/2, float64(slideH)/2
	x, y := cx-qrMark/2, cy-qrMark/2
	dc.SetRGB(1, 1, 1)
	dc.DrawRoundedRectangle(x, y, qrMark, qrMark, qrMarkRadius)
	dc.Fill()
	if logo.Mode == LogoCustom && (logo.Image != nil || logo.URL != "") {
		img, err := logo.Image, error(nil)
		if img == nil {
			img, err = fetchAvatar(logo.URL)
		}
		if err == nil {
			const box = qrLogoBox
			b := img.Bounds()
			scale := math.Min(box/float64(b.Dx()), box/float64(b.Dy()))
			dc.Push()
			dc.DrawRoundedRectangle(x+(qrMark-box)/2, y+(qrMark-box)/2, box, box, qrMarkRadius/2)
			dc.Clip()
			dc.Translate(cx, cy)
			dc.Scale(scale, scale)
			dc.DrawImageAnchored(img, 0, 0, 0.5, 0.5)
			dc.Pop()
			dc.ResetClip()
			return
		}
	}
	drawPlane(dc, cx, cy, qrMark-2*qrMarkInset, 45, accent)
}

// RenderQRSlide draws the projectable 1920x1080 slide with the Launch Crew
// plane over the code. See RenderQRSlideLogo to change or drop the mark.
func RenderQRSlide(title, shortURL string) ([]byte, error) {
	return RenderQRSlideLogo(title, shortURL, SlideLogo{})
}

// RenderQRSlideLogo draws the slide: title, QR of shortURL (error correction
// Highest, a tile with logo over the centre) and the URL below.
func RenderQRSlideLogo(title, shortURL string, logo SlideLogo) ([]byte, error) {
	fs, err := loadFonts()
	if err != nil {
		return nil, err
	}
	q, err := qrcode.New(shortURL, qrcode.Highest)
	if err != nil {
		return nil, fmt.Errorf("qr: %w", err)
	}
	var qimg image.Image = q.Image(qrSide)

	dc := gg.NewContext(slideW, slideH)
	setHex(dc, paper)
	dc.Clear()

	title = keepGlyphs(fs.display, title)
	if title == "" {
		title = "Launch Crew"
	}
	const maxW = slideW - 2*96
	var tf = must(face(fs.display, 96))
	for px := 96.0; px >= 48; px -= 4 {
		tf = must(face(fs.display, px))
		if textWidth(tf, title) <= maxW {
			break
		}
	}
	title = ellipsize(tf, title, maxW)
	dc.SetFontFace(tf)
	setHex(dc, ink)
	dc.DrawStringAnchored(title, slideW/2, 190, 0.5, 0)

	qx, qy := (slideW-qrSide)/2, (slideH-qrSide)/2
	dc.DrawImage(qimg, qx, qy)
	drawQRMark(dc, logo)

	mf := must(face(fs.mono, 40))
	dc.SetFontFace(mf)
	setHex(dc, ink)
	dc.DrawStringAnchored(shortURL, slideW/2, float64(qy+qrSide+90), 0.5, 0)

	setHex(dc, accent)
	dc.DrawRectangle(96, slideH-96-24, 24, 24)
	dc.Fill()
	setHex(dc, ink)
	dc.SetFontFace(must(face(fs.display, 32)))
	dc.DrawString("Launch Crew", 96+24+12, slideH-96)
	return encode(dc.Image())
}
