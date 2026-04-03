//go:build !windows && !linux && !darwin

package goscap

import "errors"

func newPlatformCapturer(_ *Options) (Capturer, error) {
	return nil, errors.New("capturer is only supported on Windows, Linux, and macOS")
}
