// genassets 生成 UI 使用的派生图片资源：
//   - internal/webui/web/assets/placeholder.png：无封面占位图
//   - internal/webui/web/assets/icon-grey.png：页面空状态使用的灰色图标
//
// 若项目根目录存在原始品牌素材 SimMusic.png / file.png / logo.png，
// 还会重新生成 rsrc_windows_amd64.syso（exe 图标）与侧边栏 Logo、托盘图标。
//
// 用法（在项目根目录）：go run ./cmd/genassets
package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"path/filepath"

	"github.com/tc-hib/winres"
	"golang.org/x/image/draw"
)

const outDir = "internal/webui/web/assets"

func load(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		log.Fatal(err)
	}
	return img
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func resize(img image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
	return dst
}

func square(img image.Image) image.Image {
	b := img.Bounds()
	n := max(b.Dx(), b.Dy())
	dst := image.NewRGBA(image.Rect(0, 0, n, n))
	draw.Draw(dst, b.Sub(b.Min).Add(image.Pt((n-b.Dx())/2, (n-b.Dy())/2)), img, b.Min, draw.Src)
	return dst
}

func savePNG(path string, img image.Image) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}

// placeholder 还原旧版占位封面：白底 + 10% 灰 + 居中的白色图标。
func placeholder() {
	const n = 256
	dst := image.NewRGBA(image.Rect(0, 0, n, n))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.RGBA{0xF2, 0xF2, 0xF2, 0xFF}), image.Point{}, draw.Src)
	glyph := load("assets/src/placeholder-glyph.png")
	// 原 SVG 1893×1893，字形 907×945 位于 (494, 474)
	s := float64(n) / 1893
	r := image.Rect(int(494*s), int(474*s), int((494+907)*s), int((474+945)*s))
	draw.CatmullRom.Scale(dst, r, glyph, glyph.Bounds(), draw.Over, nil)
	savePNG(filepath.Join(outDir, "placeholder.png"), dst)
}

// greyIcon 从程序图标中提取白色符号，生成灰色（#F2F2F2）透明图标。
func greyIcon() {
	src := load("assets/src/icon-256.png")
	b := src.Bounds()
	dst := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, _, a := src.At(x, y).RGBA()
			if a == 0 {
				continue
			}
			// 背景蓝色 R≈0x1F，符号为白色：以 R、G 通道估计「白度」
			w := (float64(r>>8) - 0x1F) / (0xFF - 0x1F)
			w = min(w, (float64(g>>8)-0x9F)/(0xFF-0x9F))
			if w <= 0 {
				continue
			}
			dst.SetNRGBA(x, y, color.NRGBA{0xE6, 0xE6, 0xE8, uint8(min(1, w) * 255)})
		}
	}
	// 裁掉圆角背景外的留白，使图标本身占满
	savePNG(filepath.Join(outDir, "icon-grey.png"), dst)
}

func brand() {
	if !exists("SimMusic.png") || !exists("file.png") || !exists("logo.png") {
		log.Println("未找到原始品牌素材，跳过 exe 图标 / Logo 生成")
		return
	}
	appImg := square(load("SimMusic.png"))
	fileImg := square(load("file.png"))
	logo := load("logo.png")
	sizes := []int{16, 20, 24, 32, 40, 48, 64, 256}
	appIcon, err := winres.NewIconFromResizedImage(appImg, sizes)
	if err != nil {
		log.Fatal(err)
	}
	fileIcon, err := winres.NewIconFromResizedImage(fileImg, sizes)
	if err != nil {
		log.Fatal(err)
	}
	rs := &winres.ResourceSet{}
	rs.SetIcon(winres.ID(1), appIcon)
	rs.SetIcon(winres.ID(2), fileIcon)
	rs.SetManifest(winres.AppManifest{
		Description:         "SimMusic Next",
		DPIAwareness:        winres.DPIPerMonitorV2,
		UseCommonControlsV6: true,
		LongPathAware:       true,
	})
	out, err := os.Create("rsrc_windows_amd64.syso")
	if err != nil {
		log.Fatal(err)
	}
	if err := rs.WriteObject(out, winres.ArchAMD64); err != nil {
		log.Fatal(err)
	}
	out.Close()
	lb := logo.Bounds()
	savePNG(filepath.Join(outDir, "logo.png"), resize(logo, lb.Dx()*96/lb.Dy(), 96))
	savePNG(filepath.Join(outDir, "icon.png"), resize(appImg, 256, 256))
	savePNG("assets/src/icon-256.png", resize(appImg, 256, 256))
	var ico bytes.Buffer
	trayIcon, _ := winres.NewIconFromResizedImage(appImg, []int{16, 20, 24, 32, 48})
	trayIcon.SaveICO(&ico)
	os.WriteFile(filepath.Join(outDir, "tray.ico"), ico.Bytes(), 0o644)
}

func main() {
	os.MkdirAll(outDir, 0o755)
	brand()
	placeholder()
	greyIcon()
	log.Println("assets generated")
}
