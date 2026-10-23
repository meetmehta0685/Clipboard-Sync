package clipboard

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // register decoders
	_ "image/jpeg" // register decoders
	"image/png"

	_ "golang.org/x/image/bmp"  // Windows DIB-ish data
	_ "golang.org/x/image/tiff" // macOS screenshots
)

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// NormalizeImage converts any supported image encoding (TIFF, BMP, JPEG, GIF)
// into PNG so every platform receives the same format. PNG input is returned
// unchanged.
func NormalizeImage(data []byte) ([]byte, error) {
	if bytes.HasPrefix(data, pngMagic) {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}
