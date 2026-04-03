//go:build linux

package goscap

import "os"

func newPlatformCapturer(opts *Options) (Capturer, error) {
	// Prefer PipeWire on Wayland.
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		c, err := newPipewireCapturer(opts)
		if err == nil {
			return c, nil
		}
		if os.Getenv("DISPLAY") != "" {
			if x11c, xerr := newX11Capturer(opts); xerr == nil {
				return x11c, nil
			}
		}
		return nil, err
	}
	// X11 fallback (including VNC).
	if os.Getenv("DISPLAY") != "" {
		return newX11Capturer(opts)
	}
	// Last resort: try PipeWire even without WAYLAND_DISPLAY.
	return newPipewireCapturer(opts)
}
