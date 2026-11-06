package cards

import (
	"bytes"
	"image"
	"net/url"
	"strings"

	xdraw "golang.org/x/image/draw"
)

// LogoSide is the size a captain's logo is stored at: twice the stub tile.
const LogoSide = 208

// RepoBrand is a repo's card branding: the captain's stored logo and accent,
// with the GitHub owner's avatar standing in for a missing logo.
func RepoBrand(fullName string, logoPNG []byte, accent string) Brand {
	owner, _, _ := strings.Cut(fullName, "/")
	b := Brand{Owner: owner, Accent: accent}
	if len(logoPNG) > 0 {
		if img, _, err := image.Decode(bytes.NewReader(logoPNG)); err == nil {
			b.Logo = img
		}
	}
	if b.Logo == nil && owner != "" {
		b.LogoURL = "https://github.com/" + url.PathEscape(owner) + ".png?size=" + "208"
	}
	return b
}

// ShrinkLogo scales a logo so neither side exceeds LogoSide, keeping its
// aspect, and returns it as PNG. Smaller images pass through re-encoded.
func ShrinkLogo(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if b.Dx() > LogoSide || b.Dy() > LogoSide {
		w, h := LogoSide, b.Dy()*LogoSide/b.Dx()
		if b.Dy() > b.Dx() {
			w, h = b.Dx()*LogoSide/b.Dy(), LogoSide
		}
		dst := image.NewNRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
		img = dst
	}
	return encode(img)
}
