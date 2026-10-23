package clipboard

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// FileClipboard stores text in a file. It lets you run several daemons on
// one machine (which would otherwise share one system clipboard) and drive
// them from a shell: `echo hi > a.txt` "copies" on device A.
type FileClipboard struct {
	Path string
	mu   sync.Mutex
	n    int64
	last string
}

func (f *FileClipboard) Read() (Content, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Content{}, nil
	}
	return Content{Type: Text, Data: b}, err
}

func (f *FileClipboard) Write(c Content) error {
	if c.Type != Text {
		return errors.New("clipboard: file clipboard only holds text")
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, c.Data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// ChangeCount changes whenever the file's size or modification time does.
func (f *FileClipboard) ChangeCount() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	fi, err := os.Stat(f.Path)
	key := ""
	if err == nil {
		key = fmt.Sprintf("%d/%d", fi.ModTime().UnixNano(), fi.Size())
	}
	if key != f.last {
		f.last = key
		f.n++
	}
	return f.n
}
