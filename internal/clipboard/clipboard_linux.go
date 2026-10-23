//go:build linux

package clipboard

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// System returns the Wayland clipboard (via wl-clipboard) when running under
// Wayland, and the X11 clipboard otherwise.
func System() (Clipboard, error) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("wl-paste"); err == nil {
			return wayland{}, nil
		}
		if !hasX11() {
			return nil, errors.New("clipboard: Wayland detected; install wl-clipboard (wl-paste/wl-copy)")
		}
	}
	return systemX11()
}

func hasX11() bool { return os.Getenv("DISPLAY") != "" }

// wayland shells out to wl-paste/wl-copy. Polling wl-paste every few hundred
// milliseconds is cheap enough and avoids a long-lived `wl-paste --watch`
// process per format.
type wayland struct{}

func (wayland) types() []string {
	out, err := exec.Command("wl-paste", "--list-types").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

func (w wayland) Read() (Content, error) {
	types := w.types()
	concealed := false
	hasText, imgType := false, ""
	for _, t := range types {
		switch {
		case t == "x-kde-passwordManagerHint":
			// KeePassXC and KDE apps advertise "secret" under this target.
			if out, err := exec.Command("wl-paste", "--no-newline", "--type", t).Output(); err == nil &&
				strings.TrimSpace(string(out)) == "secret" {
				concealed = true
			}
		case t == "text/plain;charset=utf-8" || t == "text/plain" || t == "UTF8_STRING" || t == "STRING":
			hasText = true
		case strings.HasPrefix(t, "image/") && imgType == "":
			imgType = t
		}
	}
	if hasText {
		out, err := exec.Command("wl-paste", "--no-newline", "--type", "text").Output()
		if err != nil {
			return Content{}, err
		}
		return Content{Type: Text, Data: out, Concealed: concealed}, nil
	}
	if imgType != "" {
		out, err := exec.Command("wl-paste", "--type", imgType).Output()
		if err != nil {
			return Content{}, err
		}
		png, err := NormalizeImage(out)
		if err != nil {
			return Content{}, err
		}
		return Content{Type: Image, Data: png, Concealed: concealed}, nil
	}
	return Content{Concealed: concealed}, nil
}

func (wayland) Write(c Content) error {
	var cmd *exec.Cmd
	switch c.Type {
	case Text:
		cmd = exec.Command("wl-copy", "--type", "text/plain;charset=utf-8")
	case Image:
		cmd = exec.Command("wl-copy", "--type", "image/png")
	default:
		return errors.New("clipboard: can only write text or images")
	}
	cmd.Stdin = bytes.NewReader(c.Data)
	return cmd.Run()
}
