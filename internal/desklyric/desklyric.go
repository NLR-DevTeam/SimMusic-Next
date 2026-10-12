package desklyric

import "image/color"

// Options 控制桌面歌词外观（尺寸单位为 DIP，与旧版 CSS 像素一致）。
type Options struct {
	Color       color.NRGBA
	Stroke      bool
	StrokeColor color.NRGBA
	Size        float64 // 字号
	Width       float64 // 歌词区域宽度
	Center      bool
	ShowTrans   bool
	TransScale  float64 // 翻译字号（相对主歌词）
	HideCapture bool
	Compact     bool
}

// Word is one timed piece of the line shown.
type Word struct {
	Start, End float64
	Text       string
}

// 桌面歌词上的控件，按显示顺序；OnControl 以这些名称报告点击，另有滚轮的 volumeUp / volumeDown。
var ControlNames = []string{"mode", "previous", "toggle", "next", "close"}
