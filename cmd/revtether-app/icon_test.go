//go:build darwin

package main

import (
	"bytes"
	"image/png"
	"testing"
)

func TestMenuBarIconPNG(t *testing.T) {
	raw := menuBarIconPNG()
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() < 22 || b.Dy() < 22 {
		t.Fatalf("size %dx%d", b.Dx(), b.Dy())
	}
	opaque := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a > 0 {
				opaque++
			}
		}
	}
	if opaque < 80 {
		t.Fatalf("icon looks empty: %d opaque pixels", opaque)
	}
}
