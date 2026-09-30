// Package edit provides format-preserving editors for the config files that
// coding agents keep: JSON/JSONC, TOML and YAML. Only the requested key
// changes; comments, ordering and indentation are left as they are.
package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Read returns the file contents, or (nil, nil) when the file does not exist.
func Read(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// WriteAtomic writes data to path via a temp file + rename so a crash can
// never leave a half-written config behind. File mode is preserved.
func WriteAtomic(path string, data []byte) error {
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Atomically runs fn, which may write the files at paths in several steps,
// and puts each of them back as it was — its bytes and mode, or its absence
// — when fn fails, so an edit that fails part way leaves no file half made.
// A file that can't be read beforehand is left to fn as it is.
func Atomically(fn func() error, paths ...string) error {
	type saved struct {
		path   string
		data   []byte
		mode   fs.FileMode
		exists bool
	}
	var before []saved
	for _, p := range paths {
		st, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			before = append(before, saved{path: p})
			continue
		}
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		before = append(before, saved{p, b, st.Mode().Perm(), true})
	}
	err := fn()
	if err == nil {
		return nil
	}
	for _, s := range before {
		now, rerr := os.ReadFile(s.path)
		switch {
		case !s.exists:
			if !errors.Is(rerr, fs.ErrNotExist) {
				if e := os.Remove(s.path); e != nil && !errors.Is(e, fs.ErrNotExist) {
					err = errors.Join(err, fmt.Errorf("put %s back: %w", s.path, e))
				}
			}
		case rerr != nil || !bytes.Equal(now, s.data):
			if e := WriteAtomic(s.path, s.data); e != nil {
				err = errors.Join(err, fmt.Errorf("put %s back: %w", s.path, e))
			} else if e := os.Chmod(s.path, s.mode); e != nil {
				err = errors.Join(err, fmt.Errorf("put %s back: %w", s.path, e))
			}
		}
	}
	return err
}
