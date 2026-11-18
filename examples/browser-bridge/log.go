// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"errors"
	"os"
	"sync"
)

const logLimit = 1 << 20

// Each Write is a complete JSON log record. The previous log is kept as .1.
type rotatingLog struct {
	mu   sync.Mutex
	path string
	size int64
	max  int64
	file *os.File
}

func openRotatingLog(path string, max int64) (*rotatingLog, error) {
	log := &rotatingLog{path: path, max: max}
	if err := log.open(); err != nil {
		return nil, err
	}
	return log, nil
}

func (l *rotatingLog) open() error {
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return errors.New("completion log must be a regular file")
	}
	l.file, l.size = file, info.Size()
	return nil
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return 0, os.ErrClosed
	}
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		err := l.file.Close()
		l.file = nil
		if err == nil {
			err = os.Rename(l.path, l.path+".1")
		}
		if reopenErr := l.open(); reopenErr != nil {
			return 0, errors.Join(err, reopenErr)
		}
		if err != nil {
			// Preserve the record in the current log if rotation was temporarily unavailable.
			n, writeErr := l.file.Write(p)
			l.size += int64(n)
			return n, errors.Join(err, writeErr)
		}
	}
	n, err := l.file.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
