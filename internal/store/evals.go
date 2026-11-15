package store

import (
	"fmt"
	"os"
	"path/filepath"
)

const evalsFile = "evals.yaml"

// EvalsPath is where a project's regression cases live.
//
// They sit next to agent.yaml rather than inside it: cases are test data, and a
// manifest that grows test data stops being something you can read in one screen.
// The export copies both files, so a shipped project carries its own checks.
func (s *Store) EvalsPath(slug string) string {
	return filepath.Join(s.ProjectDir(slug), evalsFile)
}

// LoadEvals returns the raw cases file. A project without cases is not an error:
// it returns empty bytes so the caller can show an empty editor.
func (s *Store) LoadEvals(slug string) ([]byte, error) {
	if err := checkSlug(slug); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.EvalsPath(slug))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return raw, nil
}

// SaveEvals writes the cases file, replacing it atomically.
func (s *Store) SaveEvals(slug string, raw []byte) error {
	if err := checkSlug(slug); err != nil {
		return err
	}
	if _, err := os.Stat(s.ProjectDir(slug)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("save evals for %q: %w", slug, ErrNotFound)
		}
		return err
	}
	return writeAtomic(s.EvalsPath(slug), raw)
}
