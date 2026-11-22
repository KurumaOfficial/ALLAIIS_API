// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RotatingFile is an io.Writer that keeps a log file under a size cap by moving
// it aside. Small on purpose: a long-lived gateway must not fill a disk.
type RotatingFile struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	keep    int
	size    int64
	file    *os.File
}

// NewRotatingFile opens path, creating its directory. maxSize is in bytes and
// keep is how many older files stay.
func NewRotatingFile(path string, maxSize int64, keep int) (*RotatingFile, error) {
	if maxSize <= 0 {
		maxSize = 16 << 20
	}
	if keep < 1 {
		keep = 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	writer := &RotatingFile{path: path, maxSize: maxSize, keep: keep}
	if info, err := os.Stat(path); err == nil {
		writer.size = info.Size()
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	writer.file = file
	return writer, nil
}

// Write appends, rotating first when the cap would be passed.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > r.maxSize {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	written, err := r.file.Write(p)
	r.size += int64(written)
	return written, err
}

// rotate renames the current file aside and starts a new one.
func (r *RotatingFile) rotate() error {
	r.file.Close()
	// Drop the oldest, then shift the rest down.
	os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
	for index := r.keep - 1; index >= 1; index-- {
		from := fmt.Sprintf("%s.%d", r.path, index)
		to := fmt.Sprintf("%s.%d", r.path, index+1)
		if _, err := os.Stat(from); err == nil {
			os.Rename(from, to)
		}
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	r.file = file
	r.size = 0
	return nil
}

// Close releases the file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
