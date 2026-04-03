package goscap

import (
	"errors"
	"image"
	"time"
)

// Capturer captures the desktop into an RGBA image.
// Implementations are platform-specific.
type Capturer interface {
	Capture() (*image.RGBA, error)
	// CaptureWithInfo returns the captured image and whether the frame is repeated.
	// When the desktop has no new frame, will return the last image with
	// repeated=true and err=nil.
	CaptureWithInfo() (*image.RGBA, bool, error)
	Close() error
}

// Options configures the desktop capturer.
type Options struct {
	// AdapterIndex selects the GPU adapter (0 = default).
	AdapterIndex int
	// OutputIndex selects the display/output on the adapter (0 = primary output).
	OutputIndex int
	// Timeout is the maximum time to wait for a frame (100ms = default).
	Timeout time.Duration
}

var (
	// ErrTimeout is returned when a frame is not available in time.
	ErrTimeout = errors.New("capture timeout")
)

// NewCapturer creates a capturer for the primary desktop output.
func NewCapturer() (Capturer, error) {
	return NewCapturerWithOptions(nil)
}

// NewCapturerForDisplay creates a capturer for the given display index on the default adapter.
func NewCapturerForDisplay(displayIndex int) (Capturer, error) {
	return NewCapturerWithOptions(&Options{OutputIndex: displayIndex})
}

// NewCapturerWithOptions creates a capturer with custom options.
func NewCapturerWithOptions(opts *Options) (Capturer, error) {
	normalized := normalizeOptions(opts)
	return newPlatformCapturer(normalized)
}

func normalizeOptions(opts *Options) *Options {
	if opts == nil {
		opts = &Options{}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 100 * time.Millisecond
	}
	return opts
}
