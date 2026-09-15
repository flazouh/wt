package registry

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/flazouh/wt/internal/pool"
)

func store(t *testing.T) *Store {
	t.Helper()
	t.Setenv("WT_STATE_DIR", t.TempDir())
	s, err := Open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestAnAbsentRegistryReadsAsEmptyRatherThanFailing(t *testing.T) {
	s := store(t)

	reg, err := s.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(reg.Pools) != 0 {
		t.Fatalf("a fresh registry holds %d pools", len(reg.Pools))
	}
}

// Two agents asking at once is the normal case, not an edge case. Every writer
// must see the previous writer's work, or the pool silently exceeds its cap.
func TestConcurrentWritersDoNotLoseEachOther(t *testing.T) {
	s := store(t)
	const writers = 20

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = s.Update(func(reg *Registry) error {
				p := reg.For("/repo")
				p.Slots = append(p.Slots, &pool.Slot{Index: n})
				return nil
			})
		}(i)
	}
	wg.Wait()

	reg, err := s.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := len(reg.For("/repo").Slots); got != writers {
		t.Fatalf("%d of %d writes survived; the lock is not holding", got, writers)
	}
}

// A change that fails must leave the registry untouched, which is what lets a
// failed safety check abort without half-applying.
func TestAFailedChangeWritesNothing(t *testing.T) {
	s := store(t)
	if err := s.Update(func(reg *Registry) error {
		reg.For("/repo").Slots = append(reg.For("/repo").Slots, &pool.Slot{Index: 1})
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := s.Update(func(reg *Registry) error {
		reg.For("/repo").Slots = nil
		return os.ErrPermission
	})
	if err == nil {
		t.Fatal("update swallowed the error")
	}

	reg, _ := s.Read()
	if len(reg.For("/repo").Slots) != 1 {
		t.Fatal("a failed change was written anyway")
	}
}

func TestACorruptRegistrySaysSoRatherThanStartingOver(t *testing.T) {
	s := store(t)
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := s.Read()

	if err == nil {
		t.Fatal("a corrupt registry read as empty; the pool would forget every worktree it owns")
	}
}

// The write lands through a rename, so an interrupted write cannot leave a
// half-written registry behind.
func TestWritesLeaveNoTemporaryFiles(t *testing.T) {
	s := store(t)
	if err := s.Update(func(reg *Registry) error {
		reg.For("/repo").Slots = append(reg.For("/repo").Slots, &pool.Slot{Index: 1})
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" && e.Name() != "registry.json" {
			t.Errorf("left a temporary file behind: %s", e.Name())
		}
	}
}

func TestForReturnsTheSamePoolTwice(t *testing.T) {
	reg := empty()

	first := reg.For("/repo")
	first.Slots = append(first.Slots, &pool.Slot{Index: 1})

	if len(reg.For("/repo").Slots) != 1 {
		t.Fatal("For built a second pool for the same repository")
	}
}
