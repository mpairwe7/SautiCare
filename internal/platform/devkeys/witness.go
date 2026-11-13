package devkeys

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FSWitness writes signed checkpoints to a directory, write-once (O_EXCL,
// mode 0400). In production the same port points at the backup server's
// snapshot path and the daily external witness (spec §6.3, O10).
type FSWitness struct{ dir string }

// NewFSWitness returns a witness rooted at dir.
func NewFSWitness(dir string) *FSWitness { return &FSWitness{dir: dir} }

// ReadAll returns every witnessed note (checkpoint-*.txt), oldest name first.
// An absent directory means no witness yet.
func (w *FSWitness) ReadAll() ([][]byte, error) {
	matches, err := filepath.Glob(filepath.Join(w.dir, "checkpoint-*.txt"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	out := make([][]byte, 0, len(matches))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("witness: %w", err)
		}
		out = append(out, b)
	}
	return out, nil
}

// Put stores data under name, failing if name already exists.
func (w *FSWitness) Put(_ context.Context, name string, data []byte) error {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return errors.New("witness: invalid name")
	}
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return err
	}
	// Write and sync a temporary file first, then publish it with a hard
	// link, which fails if the name exists (never replaces) and is atomic:
	// ReadAll can never observe a partial note, and a failed write leaves
	// nothing under the final name (CodeRabbit PR #2).
	final := filepath.Join(w.dir, name)
	tmp, err := os.CreateTemp(w.dir, ".tmp-"+name+"-*")
	if err != nil {
		return fmt.Errorf("witness: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("witness: %w", err)
	}
	if err := tmp.Chmod(0o400); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("witness: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("witness: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("witness: %w", err)
	}
	if err := os.Link(tmp.Name(), final); err != nil {
		return fmt.Errorf("witness: %w", err)
	}
	if d, err := os.Open(w.dir); err == nil {
		_ = d.Sync() // persist the new directory entry
		_ = d.Close()
	}
	return nil
}
