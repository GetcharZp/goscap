//go:build windows

package goscap

func newPlatformCapturer(opts *Options) (Capturer, error) {
	return newDXGICapturer(opts)
}
