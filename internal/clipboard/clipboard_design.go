//go:build (windows || linux) && cgo

package clipboard

import (
	"context"
	"errors"
	"sync"
	"time"

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

// timeout bounds each clipboard call; X11 selection owners can stall.
const timeout = 2 * time.Second

func (d *designClipboard) read(f xclip.Format) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	b, err := xclip.Read(ctx, f)
	if errors.Is(err, xclip.ErrNoData) {
		return nil, nil
	}
	return b, err
}

func (d *designClipboard) Read() (Content, error) {
	concealed := d.concealed != nil && d.concealed()
	b, err := d.read(xclip.FmtText)
	if err != nil {
		return Content{}, err
	}
	if len(b) > 0 {
		return Content{Type: Text, Data: b, Concealed: concealed}, nil
	}
	if b, err = d.read(xclip.FmtImage); err != nil {
		return Content{}, err
	}
	if len(b) > 0 {
		png, err := NormalizeImage(b)
		if err != nil {
			return Content{}, err
		}
		return Content{Type: Image, Data: png, Concealed: concealed}, nil
	}
	return Content{Concealed: concealed}, nil
}

func (d *designClipboard) Write(c Content) error {
	f := xclip.FmtText
	switch c.Type {
	case Text:
	case Image:
		f = xclip.FmtImage
	default:
		return errors.New("clipboard: can only write text or images")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := xclip.Write(ctx, f, c.Data)
	return err
}

// designCounted adds a ChangeCounter when the platform has one.
type designCounted struct{ *designClipboard }

func (d designCounted) ChangeCount() int64 { return d.counter() }
