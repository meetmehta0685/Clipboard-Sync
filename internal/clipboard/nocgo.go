//go:build !cgo && !linux

package clipboard

// System is unavailable without cgo on this platform. The relay server and
// tests still build; the daemon reports a clear error.
func System() (Clipboard, error) { return nil, ErrUnsupported }
