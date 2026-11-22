//go:build windows

// Package wincap copies what a window shows, or what a paint function
// draws, into an image. It is shared by the session backends.
package wincap

import (
	"errors"
	"image"
	"syscall"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procGetDC          = user32.NewProc("GetDC")
	procReleaseDC      = user32.NewProc("ReleaseDC")
	procGetClientRect  = user32.NewProc("GetClientRect")
	procClientToScreen = user32.NewProc("ClientToScreen")
	procPrintWindow    = user32.NewProc("PrintWindow")

	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procBitBlt             = gdi32.NewProc("BitBlt")
	procGdiFlush           = gdi32.NewProc("GdiFlush")
)

const (
	pwClientOnly        = 0x1
	pwRenderFullContent = 0x2
	srcCopy             = 0x00CC0020
	captureBlt          = 0x40000000
)

type rect struct{ left, top, right, bottom int32 }

type point struct{ x, y int32 }

// bitmapInfoHeader is BITMAPINFOHEADER (40 bytes).
type bitmapInfoHeader struct {
	size          uint32
	width, height int32
	planes, bits  uint16
	compression   uint32
	sizeImage     uint32
	xppm, yppm    int32
	used, needed  uint32
}

// Draw calls paint with a memory device context of width x height pixels
// and returns what it drew.
func Draw(width, height int, paint func(hdc uintptr)) (*image.RGBA, error) {
	if width <= 0 || height <= 0 {
		return nil, errors.New("nothing to capture: the window has no size")
	}
	screen, _, _ := procGetDC.Call(0)
	if screen == 0 {
		return nil, errors.New("GetDC failed")
	}
	defer procReleaseDC.Call(0, screen)
	mem, _, _ := procCreateCompatibleDC.Call(screen)
	if mem == 0 {
		return nil, errors.New("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(mem)

	// A top-down 32 bit DIB: the pixels are BGRX, row after row.
	bmi := bitmapInfoHeader{width: int32(width), height: -int32(height), planes: 1, bits: 32}
	bmi.size = uint32(unsafe.Sizeof(bmi))
	var bits unsafe.Pointer
	bmp, _, _ := procCreateDIBSection.Call(mem, uintptr(unsafe.Pointer(&bmi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return nil, errors.New("CreateDIBSection failed")
	}
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelectObject.Call(mem, bmp)
	defer procSelectObject.Call(mem, old)

	paint(mem)
	procGdiFlush.Call()

	src := unsafe.Slice((*byte)(bits), width*height*4)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < len(src); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = src[i+2], src[i+1], src[i], 0xFF
	}
	return img, nil
}

// Window returns the client area of a window as it is on screen. It asks
// the window to print itself (which also gives hardware-drawn content) and
// copies from the screen when that gives nothing.
func Window(hwnd uintptr) (*image.RGBA, error) {
	var rc rect
	if r, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc))); r == 0 {
		return nil, errors.New("GetClientRect failed")
	}
	w, h := int(rc.right-rc.left), int(rc.bottom-rc.top)

	printed := false
	img, err := Draw(w, h, func(hdc uintptr) {
		r, _, _ := procPrintWindow.Call(hwnd, hdc, pwClientOnly|pwRenderFullContent)
		printed = r != 0
	})
	if err != nil {
		return nil, err
	}
	if printed && !blank(img) {
		return img, nil
	}

	var pt point
	procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
	copied := false
	img, err = Draw(w, h, func(hdc uintptr) {
		screen, _, _ := procGetDC.Call(0)
		if screen == 0 {
			return
		}
		defer procReleaseDC.Call(0, screen)
		r, _, _ := procBitBlt.Call(hdc, 0, 0, uintptr(w), uintptr(h), screen, uintptr(pt.x), uintptr(pt.y), srcCopy|captureBlt)
		copied = r != 0
	})
	if err != nil {
		return nil, err
	}
	if !copied {
		return nil, errors.New("could not copy the window from the screen")
	}
	return img, nil
}

// blank reports whether every pixel has the same colour (a failed print).
func blank(img *image.RGBA) bool {
	p := img.Pix
	for i := 4; i < len(p); i += 4 {
		if p[i] != p[0] || p[i+1] != p[1] || p[i+2] != p[2] {
			return false
		}
	}
	return true
}
