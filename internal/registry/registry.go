// Package registry stores the pools and keeps concurrent agents from treading
// on each other.
//
// Two agents asking for a worktree at the same moment is the normal case here,
// not an edge case, so every read-modify-write runs under an exclusive file
// lock and every write lands through a temporary file and a rename. A crash
// mid-write leaves the previous registry intact rather than a half-written one.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/flazouh/wt/internal/pool"
)

// Registry is every pool on the machine, keyed by the main checkout's path.
type Registry struct {
	Version int                   `json:"version"`
	Pools   map[string]*pool.Pool `json:"pools"`
}

// Store is the registry on disk.
type Store struct {
	path string
}

// Open returns the store at the standard location, creating its directory.
func Open() (*Store, error) {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	return &Store{path: filepath.Join(dir, "registry.json")}, nil
}

// Path is where the registry lives, for the home view to report.
func (s *Store) Path() string { return s.path }

func stateDir() string {
	if dir := os.Getenv("WT_STATE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "wt")
	}
	return filepath.Join(home, ".local", "state", "wt")
}

// Read returns the registry without holding a lock. For reporting only: a
// caller that intends to write must use Update.
func (s *Store) Read() (*Registry, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return empty(), nil
	}
	if err != nil {
		return nil, err
	}
	return decode(data)
}

// Update runs change against the registry under an exclusive lock and writes
// the result. The lock is held for the whole call, so change may run git.
//
// When change returns an error nothing is written, which is what makes a failed
// safety check leave the registry exactly as it was.
func (s *Store) Update(change func(*Registry) error) error {
	lock, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("opening the registry lock: %w", err)
	}
	defer lock.Close()

	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("locking the registry: %w", err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()

	reg, err := s.Read()
	if err != nil {
		return err
	}
	if err := change(reg); err != nil {
		return err
	}
	return s.write(reg)
}

func (s *Store) write(reg *Registry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// Same directory, so the rename is atomic rather than a cross-device copy.
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".registry-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func empty() *Registry {
	return &Registry{Version: 1, Pools: map[string]*pool.Pool{}}
}

func decode(data []byte) (*Registry, error) {
	reg := empty()
	if len(data) == 0 {
		return reg, nil
	}
	if err := json.Unmarshal(data, reg); err != nil {
		return nil, fmt.Errorf("the registry is not readable: %w", err)
	}
	if reg.Pools == nil {
		reg.Pools = map[string]*pool.Pool{}
	}
	return reg, nil
}

// For returns the pool for a repository, creating an empty one.
func (r *Registry) For(repo string) *pool.Pool {
	if p, ok := r.Pools[repo]; ok {
		return p
	}
	p := &pool.Pool{Repo: repo}
	r.Pools[repo] = p
	return p
}
