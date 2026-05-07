//go:build darwin

package goscap

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -Wno-unguarded-availability
#cgo LDFLAGS: -framework ScreenCaptureKit -framework CoreMedia -framework CoreVideo -framework Foundation
#include <CoreVideo/CoreVideo.h>
#include "sckit.h"
*/
import "C"

import (
	"errors"
	"image"
	"sync"
	"time"
	"unsafe"
)

type screenCaptureKitCapturer struct {
	mu        sync.Mutex
	timeout   time.Duration
	cap       *C.sc_capture
	lastImage *image.RGBA
	lastSeq   uint64
}

func newScreenCaptureKitCapturer(opts *Options) (Capturer, error) {
	cap := C.sc_capture_new(C.uint32_t(opts.OutputIndex))
	if cap == nil {
		return nil, errors.New("screencapturekit init failed")
	}
	return &screenCaptureKitCapturer{timeout: opts.Timeout, cap: cap}, nil
}

func (c *screenCaptureKitCapturer) Capture() (*image.RGBA, error) {
	img, _, err := c.CaptureWithInfo()
	return img, err
}

func (c *screenCaptureKitCapturer) CaptureWithInfo() (*image.RGBA, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cap == nil {
		return nil, false, errors.New("screencapturekit not initialized")
	}

	timeout := c.timeout
	if c.lastImage != nil {
		timeout = 0
	}

	var frame C.sc_frame
	ret := C.sc_capture_read(c.cap, C.uint32_t(timeout.Milliseconds()), C.uint64_t(c.lastSeq), &frame)
	if ret == 1 {
		if c.lastImage != nil {
			return c.lastImage, true, nil
		}
		return nil, false, ErrTimeout
	}
	if ret != 0 {
		return nil, false, errors.New("screencapturekit capture error")
	}
	defer C.sc_capture_free_frame(&frame)

	var buf []byte
	if frame.size > 0 {
		if frame.data == nil {
			return nil, false, errors.New("invalid screencapturekit frame buffer")
		}
		buf = unsafe.Slice((*byte)(unsafe.Pointer(frame.data)), int(frame.size))
	}
	img, err := convertMacFrame(&frame, buf, c.ensureImage(int(frame.width), int(frame.height)))
	if err != nil {
		return nil, false, err
	}
	c.lastSeq = uint64(frame.seq)
	c.lastImage = img
	return img, false, nil
}

func (c *screenCaptureKitCapturer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cap != nil {
		C.sc_capture_destroy(c.cap)
		c.cap = nil
	}
	return nil
}

func (c *screenCaptureKitCapturer) ensureImage(width, height int) *image.RGBA {
	if c.lastImage != nil && c.lastImage.Rect.Dx() == width && c.lastImage.Rect.Dy() == height {
		return c.lastImage
	}
	return image.NewRGBA(image.Rect(0, 0, width, height))
}

func convertMacFrame(frame *C.sc_frame, buf []byte, img *image.RGBA) (*image.RGBA, error) {
	width := int(frame.width)
	height := int(frame.height)
	stride := int(frame.stride)
	if width <= 0 || height <= 0 || stride <= 0 {
		return nil, errors.New("invalid screencapturekit frame")
	}
	if stride*height > len(buf) {
		return nil, errors.New("invalid screencapturekit frame buffer")
	}
	for y := 0; y < height; y++ {
		srcOff := y * stride
		dstOff := y * img.Stride
		src := buf[srcOff : srcOff+width*4]
		dst := img.Pix[dstOff : dstOff+width*4]
		switch uint32(frame.format) {
		case uint32(C.kCVPixelFormatType_32BGRA):
			for x := 0; x < width; x++ {
				i := x * 4
				dst[i+0] = src[i+2]
				dst[i+1] = src[i+1]
				dst[i+2] = src[i+0]
				dst[i+3] = 0xFF
			}
		case uint32(C.kCVPixelFormatType_32RGBA):
			for x := 0; x < width; x++ {
				i := x * 4
				dst[i+0] = src[i+0]
				dst[i+1] = src[i+1]
				dst[i+2] = src[i+2]
				dst[i+3] = 0xFF
			}
		case uint32(C.kCVPixelFormatType_32ARGB):
			for x := 0; x < width; x++ {
				i := x * 4
				dst[i+0] = src[i+1]
				dst[i+1] = src[i+2]
				dst[i+2] = src[i+3]
				dst[i+3] = 0xFF
			}
		default:
			return nil, errors.New("unsupported pixel format")
		}
	}
	return img, nil
}
