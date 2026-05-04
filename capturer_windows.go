//go:build windows

package goscap

import "fmt"

func newPlatformCapturer(opts *Options) (Capturer, error) {
	c, err := newDXGICapturer(opts)
	if err == nil {
		return c, nil
	}

	gdi, gdiErr := newGDICapturer(opts)
	if gdiErr == nil {
		return gdi, nil
	}
	return nil, fmt.Errorf("DXGI capture unavailable: %w; GDI fallback unavailable: %v", err, gdiErr)
}
