//go:build windows

package goscap

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type gdiCapturer struct {
	mu      sync.Mutex
	rect    RECT
	timeout time.Duration
}

var (
	modUser32 = syscall.NewLazyDLL("user32.dll")
	modGDI32  = syscall.NewLazyDLL("gdi32.dll")

	procGetDC                  = modUser32.NewProc("GetDC")
	procReleaseDC              = modUser32.NewProc("ReleaseDC")
	procGetSystemMetrics       = modUser32.NewProc("GetSystemMetrics")
	procEnumDisplayMonitors    = modUser32.NewProc("EnumDisplayMonitors")
	procCreateCompatibleDC     = modGDI32.NewProc("CreateCompatibleDC")
	procDeleteDC               = modGDI32.NewProc("DeleteDC")
	procCreateCompatibleBitmap = modGDI32.NewProc("CreateCompatibleBitmap")
	procCreateDIBSection       = modGDI32.NewProc("CreateDIBSection")
	procDeleteObject           = modGDI32.NewProc("DeleteObject")
	procSelectObject           = modGDI32.NewProc("SelectObject")
	procBitBlt                 = modGDI32.NewProc("BitBlt")
)

const (
	smCXScreen = 0
	smCYScreen = 1

	biRGB        = 0
	dibRGBColors = 0
	srccopy      = 0x00CC0020
	captureblt   = 0x40000000
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

type monitorEnumState struct {
	rects []RECT
}

func newGDICapturer(opts *Options) (Capturer, error) {
	rect, err := gdiDisplayRect(opts.OutputIndex)
	if err != nil {
		return nil, err
	}
	if rect.Right <= rect.Left || rect.Bottom <= rect.Top {
		return nil, errors.New("invalid GDI capture bounds")
	}
	return &gdiCapturer{rect: rect, timeout: opts.Timeout}, nil
}

func gdiDisplayRect(outputIndex int) (RECT, error) {
	if outputIndex <= 0 {
		width := int32(getSystemMetrics(smCXScreen))
		height := int32(getSystemMetrics(smCYScreen))
		return RECT{Right: width, Bottom: height}, nil
	}

	rects, err := enumDisplayRects()
	if err != nil {
		return RECT{}, err
	}
	if outputIndex >= len(rects) {
		return RECT{}, fmt.Errorf("display index %d not found", outputIndex)
	}
	return rects[outputIndex], nil
}

func enumDisplayRects() ([]RECT, error) {
	state := &monitorEnumState{}
	cb := syscall.NewCallback(func(_ uintptr, _ uintptr, rect uintptr, data uintptr) uintptr {
		s := (*monitorEnumState)(unsafe.Pointer(data))
		s.rects = append(s.rects, *(*RECT)(unsafe.Pointer(rect)))
		return 1
	})
	ret, _, err := procEnumDisplayMonitors.Call(0, 0, cb, uintptr(unsafe.Pointer(state)))
	if ret == 0 {
		return nil, syscallError("EnumDisplayMonitors", err)
	}
	return state.rects, nil
}

func getSystemMetrics(index int32) int {
	ret, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(ret)
}

func (c *gdiCapturer) Capture() (*image.RGBA, error) {
	img, _, err := c.CaptureWithInfo()
	return img, err
}

func (c *gdiCapturer) CaptureWithInfo() (*image.RGBA, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	width := int(c.rect.Right - c.rect.Left)
	height := int(c.rect.Bottom - c.rect.Top)
	if width <= 0 || height <= 0 {
		return nil, false, errors.New("invalid GDI capture bounds")
	}

	screenDC, _, err := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, false, syscallError("GetDC", err)
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, err := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, false, syscallError("CreateCompatibleDC", err)
	}
	defer procDeleteDC.Call(memDC)

	bmi := bitmapInfo{
		Header: bitmapInfoHeader{
			Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			Width:       int32(width),
			Height:      -int32(height),
			Planes:      1,
			BitCount:    32,
			Compression: biRGB,
			SizeImage:   uint32(width * height * 4),
		},
	}
	var bits uintptr
	bitmap, _, err := procCreateDIBSection.Call(
		screenDC,
		uintptr(unsafe.Pointer(&bmi)),
		dibRGBColors,
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if bitmap == 0 || bits == 0 {
		return nil, false, syscallError("CreateDIBSection", err)
	}
	defer procDeleteObject.Call(bitmap)

	oldBitmap, _, _ := procSelectObject.Call(memDC, bitmap)
	defer procSelectObject.Call(memDC, oldBitmap)

	ret, _, err := procBitBlt.Call(
		memDC,
		0,
		0,
		uintptr(width),
		uintptr(height),
		screenDC,
		uintptr(c.rect.Left),
		uintptr(c.rect.Top),
		srccopy|captureblt,
	)
	if ret == 0 {
		return nil, false, syscallError("BitBlt", err)
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	src := unsafe.Slice((*byte)(unsafe.Pointer(bits)), width*height*4)
	copy(img.Pix, src)
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+2] = img.Pix[i+2], img.Pix[i]
		img.Pix[i+3] = 0xff
	}
	return img, false, nil
}

func (c *gdiCapturer) Close() error {
	return nil
}

func syscallError(op string, err error) error {
	if err != nil && !errors.Is(err, syscall.Errno(0)) {
		return fmt.Errorf("%s failed: %w", op, err)
	}
	return errors.New(op + " failed")
}
