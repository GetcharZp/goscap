//go:build darwin

package goscap

func newPlatformCapturer(opts *Options) (Capturer, error) {
	return newScreenCaptureKitCapturer(opts)
}
