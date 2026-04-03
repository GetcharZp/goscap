<div align="center" style="text-align: center;">
  <img src="./assets/logo.png" alt="logo" width="200" style="display: block; margin: 0 auto;" />
</div>

<p align="center">
   <a href="https://github.com/getcharzp/goscap/fork" target="blank">
      <img src="https://img.shields.io/github/forks/getcharzp/goscap?style=for-the-badge" alt="goscap forks"/>
   </a>
   <a href="https://github.com/getcharzp/goscap/stargazers" target="blank">
      <img src="https://img.shields.io/github/stars/getcharzp/goscap?style=for-the-badge" alt="goscap stars"/>
   </a>
   <a href="https://github.com/getcharzp/goscap/pulls" target="blank">
      <img src="https://img.shields.io/github/issues-pr/getcharzp/goscap?style=for-the-badge" alt="goscap pull-requests"/>
   </a>
</p>


goscap (Golang Screen Capture) 是一个跨平台桌面截图库，提供统一的 API 来获取当前显示器的桌面图像。

- Windows：基于 DXGI Desktop Duplication，低延迟、高吞吐
- Linux：Wayland/Portal + PipeWire（支持授权截屏），在 X11/VNC 场景自动回退到 X11 截图
- macOS：基于 ScreenCaptureKit（macOS 12.3+）

适合屏幕录制、远程控制、桌面缩略图、实时检测等场景。

## 安装

```bash
go get -u github.com/getcharzp/goscap
```

## 快速开始

```go
package main

import (
	"github.com/getcharzp/goscap"
	"github.com/up-zero/gotool/imageutil"
)

func main() {
	cap, err := goscap.NewCapturerForDisplay(0)
	if err != nil {
		panic(err)
	}
	defer cap.Close()

	img, _, err := cap.CaptureWithInfo()
	if err != nil {
		panic(err)
	}

	imageutil.Save("screen.png", img, 100)
}
```
