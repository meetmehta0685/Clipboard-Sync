//go:build (windows || linux) && cgo

package clipboard

import (
	"errors"
	"sync"

	xclip "golang.design/x/clipboard"
)

// designClipboard wraps golang.design/x/clipboard, which covers Windows and
// X11. It prefers text over images, matching the macOS backend.
type designClipboard struct {
	concealed func() bool
	counter   func() int64
}

var initOnce sync.Once
var initErr error

func newDesign() (*designClipboard, error) {
	initOnce.Do(func() { initErr = xclip.Init() })
	if initErr != nil {
		return nil, initErr
	}
	return &designClipboard{}, nil
}

func (d *designClipboard) Read() (Content, error) {
	concealed := d.concealed != nil && d.concealed()
	if b := xclip.Read(xclip.FmtText); len(b) > 0 {
		return Content{Type: Text, Data: b, Concealed: concealed}, nil
	}
	if b := xclip.Read(xclip.FmtImage); len(b) > 0 {
		png, err := NormalizeImage(b)
		if err != nil {
			return Content{}, err
		}
		return Content{Type: Image, Data: png, Concealed: concealed}, nil
	}
	return Content{Concealed: concealed}, nil
}

func (d *designClipboard) Write(c Content) error {
	switch c.Type {
	case Text:
		xclip.Write(xclip.FmtText, c.Data)
	case Image:
		xclip.Write(xclip.FmtImage, c.Data)
	default:
		return errors.New("clipboard: can only write text or images")
	}
	return nil
}

// designCounted adds a ChangeCounter when the platform has one.
type designCounted struct{ *designClipboard }

func (d designCounted) ChangeCount() int64 { return d.counter() }
