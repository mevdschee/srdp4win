package icons

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/draw"
	"image/png"

	xdraw "golang.org/x/image/draw"
)

// The application icon is app/icon.svg. It uses gradients and a drop shadow,
// which the SVG renderer of this package cannot draw, so tools/genicon
// renders it to the PNG files embedded here (and to winres/).
//
//go:embed app/icon256.png app/icon48.png app/icon32.png app/icon16.png
var appFS embed.FS

// AppSizes are the sizes the application icon is rendered at.
var AppSizes = []int{16, 32, 48, 256}

// AppIcon returns the application icon at an exact size: one of the rendered
// sizes as it is, any other size scaled down from the next larger one.
func AppIcon(size int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	if size <= 0 {
		return out
	}
	from := AppSizes[len(AppSizes)-1]
	for _, s := range AppSizes {
		if s >= size {
			from = s
			break
		}
	}
	data, err := appFS.ReadFile(fmt.Sprintf("app/icon%d.png", from))
	if err != nil {
		return out
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return out
	}
	if from == size {
		draw.Draw(out, out.Bounds(), src, src.Bounds().Min, draw.Src)
	} else {
		xdraw.CatmullRom.Scale(out, out.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	}
	return out
}
