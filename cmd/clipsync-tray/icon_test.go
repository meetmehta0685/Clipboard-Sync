package main

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

func TestIconDecodes(t *testing.T) {
	for _, tmpl := range []bool{true, false} {
		img, err := png.Decode(bytes.NewReader(icon(tmpl)))
		if err != nil || img.Bounds().Dx() != 44 {
			t.Fatalf("bad icon: %v", err)
		}
	}
	if p := os.Getenv("WRITE_ICON"); p != "" {
		_ = os.WriteFile(p, icon(false), 0o644)
	}
}
