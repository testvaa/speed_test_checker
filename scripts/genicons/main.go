// genicons — генератор иконок трея (32x32, спидометр): состояния (.ico + .png)
// и кадры анимации «идёт проверка» (.ico).
// Запуск: go run ./scripts/genicons  (из корня модуля speedtest-tray).
package main

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const size = 32

type palette struct {
	fill   color.RGBA
	border color.RGBA
}

var (
	idle = palette{color.RGBA{R: 138, G: 145, B: 152, A: 255}, color.RGBA{R: 97, G: 103, B: 109, A: 255}}
	ok   = palette{color.RGBA{R: 52, G: 168, B: 83, A: 255}, color.RGBA{R: 30, G: 126, B: 52, A: 255}}
	warn = palette{color.RGBA{R: 249, G: 171, B: 0, A: 255}, color.RGBA{R: 198, G: 125, B: 0, A: 255}}
	bad  = palette{color.RGBA{R: 234, G: 67, B: 53, A: 255}, color.RGBA{R: 179, G: 38, B: 30, A: 255}}
	work = palette{color.RGBA{R: 74, G: 144, B: 217, A: 255}, color.RGBA{R: 44, G: 105, B: 165, A: 255}}
)

// main: состояния (ico+png), затем 8 кадров свипа стрелки для анимации.
func main() {
	out := "icons"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		fatal(err)
	}
	for name, p := range map[string]palette{"idle": idle, "ok": ok, "warn": warn, "bad": bad} {
		img := drawGauge(p, -50)
		if err := os.WriteFile(filepath.Join(out, name+".ico"), encodeICO(img), 0o644); err != nil {
			fatal(err)
		}
		f, err := os.Create(filepath.Join(out, name+".png"))
		if err != nil {
			fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			fatal(err)
		}
		f.Close()
		fmt.Println("written", name+".ico/.png")
	}
	// Кадры свипа стрелки: от 150° до -60° — «сканирование» слева направо.
	for i, ang := 0, 150.0; i < 8; i, ang = i+1, ang-30 {
		img := drawGauge(work, ang)
		path := filepath.Join(out, fmt.Sprintf("working%d.ico", i+1))
		if err := os.WriteFile(path, encodeICO(img), 0o644); err != nil {
			fatal(err)
		}
		// PNG-кадры нужны трею на macOS/Linux (ICO там не отображается)
		f, err := os.Create(filepath.Join(out, fmt.Sprintf("working%d.png", i+1)))
		if err != nil {
			fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			fatal(err)
		}
		f.Close()
		fmt.Println("written", path)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "genicons:", err)
	os.Exit(1)
}

// drawGauge рисует круглый «спидометр» с белой стрелкой под углом angleDeg
// (0° — вправо, отрицательные — вверх).
func drawGauge(p palette, angleDeg float64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	cx, cy := 15.5, 16.0
	rOut := 14.0
	const needleLen = 10.0
	ang := angleDeg * math.Pi / 180.0
	nx, ny := cx+needleLen*math.Cos(ang), cy+needleLen*math.Sin(ang)

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			d := math.Hypot(px-cx, py-cy)
			if d > rOut {
				continue // прозрачный фон
			}
			c := p.fill
			if d > rOut-2.3 {
				c = p.border
			}
			if distToSegment(px, py, cx, cy, nx, ny) <= 1.4 || d <= 2.5 {
				c = color.RGBA{R: 255, G: 255, B: 255, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func distToSegment(px, py, ax, ay, bx, by float64) float64 {
	vx, vy := bx-ax, by-ay
	l2 := vx*vx + vy*vy
	t := 0.0
	if l2 > 0 {
		t = clamp(((px-ax)*vx+(py-ay)*vy)/l2, 0, 1)
	}
	return math.Hypot(px-(ax+t*vx), py-(ay+t*vy))
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// encodeICO упаковывает RGBA-картинку в одноименного формата .ico
// (один слой 32x32, 32 bpp, с альфа-каналом).
func encodeICO(img *image.RGBA) []byte {
	const andMaskSize = size * 4 // 32 строки по 4 байта (все нули — пиксели непрозрачные)
	pixSize := size * size * 4

	entry := 22 // ICONDIR(6) + ICONDIRENTRY(16)
	bmp := make([]byte, 40+pixSize+andMaskSize)
	// BITMAPINFOHEADER: высота удвоенная (пиксели + AND-маска).
	binary.LittleEndian.PutUint32(bmp[0:], 40)
	binary.LittleEndian.PutUint32(bmp[4:], size)
	binary.LittleEndian.PutUint32(bmp[8:], size*2)
	binary.LittleEndian.PutUint16(bmp[12:], 1)
	binary.LittleEndian.PutUint16(bmp[14:], 32)
	binary.LittleEndian.PutUint32(bmp[20:], uint32(pixSize+andMaskSize))
	// Пиксели снизу вверх, порядок BGRA.
	off := 40
	for y := size - 1; y >= 0; y-- {
		for x := 0; x < size; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			bmp[off] = byte(b >> 8)
			bmp[off+1] = byte(g >> 8)
			bmp[off+2] = byte(r >> 8)
			bmp[off+3] = byte(a >> 8)
			off += 4
		}
	}

	out := make([]byte, entry, entry+len(bmp))
	binary.LittleEndian.PutUint16(out[0:], 0) // reserved
	binary.LittleEndian.PutUint16(out[2:], 1) // type: icon
	binary.LittleEndian.PutUint16(out[4:], 1) // count
	out[6] = byte(size)
	out[7] = byte(size)
	out[8] = 0                                  // colors
	out[9] = 0                                  // reserved
	binary.LittleEndian.PutUint16(out[10:], 1)  // planes
	binary.LittleEndian.PutUint16(out[12:], 32) // bpp
	binary.LittleEndian.PutUint32(out[14:], uint32(len(bmp)))
	binary.LittleEndian.PutUint32(out[18:], uint32(entry))
	return append(out, bmp...)
}
