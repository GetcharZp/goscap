package goscap

import (
	"fmt"
	"github.com/up-zero/gotool/imageutil"
	"image"
	"testing"
	"time"
)

func TestNewCapturer(t *testing.T) {
	c, err := NewCapturer()
	if err != nil {
		t.Fatalf("new capturer for display error %v", err)
	}
	defer c.Close()
	var img image.Image
	var repeated bool

	for i := 0; i < 100; i++ {
		start := time.Now()
		img, repeated, err = c.CaptureWithInfo()
		if err != nil {
			t.Fatalf("capture error %v", err)
		}
		fmt.Printf("重复：%v \t 耗时：%v\n", repeated, time.Since(start))
		time.Sleep(time.Millisecond * 20)
	}

	imageutil.Save("test.png", img, 100)
}
