//go:build linux && !cgo

package clipboard

func systemX11() (Clipboard, error) { return nil, ErrUnsupported }
