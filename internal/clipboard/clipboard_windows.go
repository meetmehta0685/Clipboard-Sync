//go:build windows && cgo

package clipboard

import (
	"syscall"
	"unsafe"
)

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	procRegisterClipboardFormatW   = user32.NewProc("RegisterClipboardFormatW")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
)

func registerFormat(name string) uintptr {
	p, _ := syscall.UTF16PtrFromString(name)
	r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(p)))
	return r
}

// Formats that password managers and Windows itself use to opt content out of
// clipboard history and monitoring.
var excludeFormats = []string{
	"ExcludeClipboardContentFromMonitorProcessing",
	"Clipboard Viewer Ignore",
}

// System returns the Windows clipboard.
func System() (Clipboard, error) {
	d, err := newDesign()
	if err != nil {
		return nil, err
	}
	var ids []uintptr
	for _, f := range excludeFormats {
		if id := registerFormat(f); id != 0 {
			ids = append(ids, id)
		}
	}
	d.concealed = func() bool {
		for _, id := range ids {
			if r, _, _ := procIsClipboardFormatAvailable.Call(id); r != 0 {
				return true
			}
		}
		return false
	}
	d.counter = func() int64 {
		r, _, _ := procGetClipboardSequenceNumber.Call()
		return int64(r)
	}
	return designCounted{d}, nil
}
