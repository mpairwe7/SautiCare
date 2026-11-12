package devkeys

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FSWitness writes signed checkpoints to a directory, write-once (O_EXCL,
// mode 0400). In production the same port points at the backup server's
// snapshot path and the daily external witness (spec §6.3, O10).
type FSWitness struct{ dir string }

// NewFSWitness returns a witness rooted at dir.
func NewFSWitness(dir string) *FSWitness { return &FSWitness{dir: dir} }

// Put stores data under name, failing if name already exists.
func (w *FSWitness) Put(_ context.Context, name string, data []byte) error {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return errors.New("witness: invalid name")
	}
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(w.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return fmt.Errorf("witness: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("witness: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("witness: %w", err)
	}
	return f.Close()
}
