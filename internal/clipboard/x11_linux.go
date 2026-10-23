//go:build linux && cgo

package clipboard

func systemX11() (Clipboard, error) {
	d, err := newDesign()
	if err != nil {
		return nil, err
	}
	return d, nil
}
