//go:build linux

package goscap

import (
	"errors"
	"image"
	"sync"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xinerama"
	"github.com/BurntSushi/xgb/xproto"
)

type x11Capturer struct {
	mu        sync.Mutex
	conn      *xgb.Conn
	root      xproto.Drawable
	x         int16
	y         int16
	width     uint16
	height    uint16
	bpp       uint8
	padBits   uint8
	byteOrder byte
}

func newX11Capturer(opts *Options) (Capturer, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	setup := xproto.Setup(conn)
	if len(setup.Roots) == 0 {
		conn.Close()
		return nil, errors.New("x11: no screens")
	}
	screen := setup.DefaultScreen(conn)
	root := screen.Root
	depth := screen.RootDepth

	bpp, pad := findPixmapFormat(setup, depth)
	if bpp == 0 {
		bpp = 32
		pad = 32
	}

	// Default to full screen.
	x := int16(0)
	y := int16(0)
	w := screen.WidthInPixels
	h := screen.HeightInPixels

	// Try Xinerama for multi-monitor selection.
	if err := xinerama.Init(conn); err == nil {
		if active, err := xinerama.IsActive(conn).Reply(); err == nil && active.State != 0 {
			if screens, err := xinerama.QueryScreens(conn).Reply(); err == nil && len(screens.ScreenInfo) > 0 {
				idx := opts.OutputIndex
				if idx < 0 || idx >= len(screens.ScreenInfo) {
					idx = 0
				}
				s := screens.ScreenInfo[idx]
				x = int16(s.XOrg)
				y = int16(s.YOrg)
				w = uint16(s.Width)
				h = uint16(s.Height)
			}
		}
	}

	return &x11Capturer{
		conn:      conn,
		root:      xproto.Drawable(root),
		x:         x,
		y:         y,
		width:     w,
		height:    h,
		bpp:       bpp,
		padBits:   pad,
		byteOrder: setup.ImageByteOrder,
	}, nil
}

func findPixmapFormat(setup *xproto.SetupInfo, depth byte) (bpp, pad uint8) {
	for _, fmt := range setup.PixmapFormats {
		if fmt.Depth == depth {
			return fmt.BitsPerPixel, fmt.ScanlinePad
		}
	}
	return 0, 0
}

func (c *x11Capturer) Capture() (*image.RGBA, error) {
	img, _, err := c.CaptureWithInfo()
	return img, err
}

func (c *x11Capturer) CaptureWithInfo() (*image.RGBA, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	reply, err := xproto.GetImage(c.conn, xproto.ImageFormatZPixmap, c.root, c.x, c.y, c.width, c.height, ^uint32(0)).Reply()
	if err != nil {
		return nil, false, err
	}
	img, err := x11ToRGBA(reply, int(c.width), int(c.height), c.bpp, c.padBits, c.byteOrder)
	if err != nil {
		return nil, false, err
	}
	return img, false, nil
}

func x11ToRGBA(reply *xproto.GetImageReply, width, height int, bpp, padBits uint8, byteOrder byte) (*image.RGBA, error) {
	if reply == nil {
		return nil, errors.New("x11: empty reply")
	}
	data := reply.Data
	if width <= 0 || height <= 0 {
		return nil, errors.New("x11: invalid dimensions")
	}
	bytesPerPixel := int(bpp) / 8
	if bytesPerPixel == 0 {
		return nil, errors.New("x11: invalid bpp")
	}
	// Compute stride based on padding.
	strideBits := (width*int(bpp) + int(padBits) - 1) / int(padBits) * int(padBits)
	stride := strideBits / 8
	if stride*height > len(data) {
		// Fallback: derive from data length.
		if height > 0 {
			stride = len(data) / height
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		srcOff := y * stride
		dstOff := y * img.Stride
		src := data[srcOff : srcOff+stride]
		dst := img.Pix[dstOff : dstOff+width*4]

		switch bytesPerPixel {
		case 4:
			if byteOrder == xproto.ImageOrderLSBFirst {
				// Little-endian: BGRA
				for x := 0; x < width; x++ {
					i := x * 4
					dst[i+0] = src[i+2]
					dst[i+1] = src[i+1]
					dst[i+2] = src[i+0]
					dst[i+3] = 0xFF
				}
			} else {
				// Big-endian: ARGB
				for x := 0; x < width; x++ {
					i := x * 4
					dst[i+0] = src[i+1]
					dst[i+1] = src[i+2]
					dst[i+2] = src[i+3]
					dst[i+3] = 0xFF
				}
			}
		case 3:
			if byteOrder == xproto.ImageOrderLSBFirst {
				// Little-endian: BGR
				for x := 0; x < width; x++ {
					i := x * 3
					j := x * 4
					dst[j+0] = src[i+2]
					dst[j+1] = src[i+1]
					dst[j+2] = src[i+0]
					dst[j+3] = 0xFF
				}
			} else {
				// Big-endian: RGB
				for x := 0; x < width; x++ {
					i := x * 3
					j := x * 4
					dst[j+0] = src[i+0]
					dst[j+1] = src[i+1]
					dst[j+2] = src[i+2]
					dst[j+3] = 0xFF
				}
			}
		default:
			return nil, errors.New("x11: unsupported bpp")
		}
	}
	return img, nil
}

func (c *x11Capturer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	return nil
}
