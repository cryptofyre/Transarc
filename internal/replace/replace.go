package replace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	phaseStaged = "staged"
	phaseMoved  = "moved"
	phaseDone   = "done"
)

// Journal is the on-disk recovery record.
type Journal struct {
	Phase    string `json:"phase"`
	Original string `json:"original"`
	Stage    string `json:"stage"`
	Backup   string `json:"backup"`
}

// Commit copies encoded into original's directory and replaces original.
// encoded is removed after a successful commit. On failure the original path
// still names the original file, or Recover can restore it.
func Commit(original, encoded string) error {
	if original == "" || encoded == "" {
		return fmt.Errorf("original and encoded paths are required")
	}
	info, err := os.Stat(original)
	if err != nil {
		return fmt.Errorf("stat original: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("original is a directory")
	}
	stage := original + ".transarc.stage"
	backup := original + ".transarc.bak"
	journalPath := original + ".transarc.journal"
	if err := place(encoded, stage); err != nil {
		return err
	}
	if err := os.Chmod(stage, info.Mode().Perm()); err != nil {
		_ = os.Remove(stage)
		return err
	}
	j := Journal{Phase: phaseStaged, Original: original, Stage: stage, Backup: backup}
	if err := writeJournal(journalPath, j); err != nil {
		_ = os.Remove(stage)
		return err
	}
	if err := os.Rename(original, backup); err != nil {
		_ = os.Remove(stage)
		_ = os.Remove(journalPath)
		return fmt.Errorf("move original aside: %w", err)
	}
	j.Phase = phaseMoved
	if err := writeJournal(journalPath, j); err != nil {
		_ = os.Rename(backup, original)
		_ = os.Remove(stage)
		_ = os.Remove(journalPath)
		return err
	}
	if err := os.Rename(stage, original); err != nil {
		_ = os.Rename(backup, original)
		_ = os.Remove(stage)
		_ = os.Remove(journalPath)
		return fmt.Errorf("move encode into place: %w", err)
	}
	j.Phase = phaseDone
	_ = writeJournal(journalPath, j)
	_ = os.Remove(backup)
	_ = os.Remove(journalPath)
	if encoded != stage && encoded != original {
		_ = os.Remove(encoded)
	}
	syncDir(filepath.Dir(original))
	return nil
}

// RecoverTree finishes or rolls back journals under root.
func RecoverTree(root string) error {
	var first error
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".transarc.journal") {
			return nil
		}
		if rerr := Recover(path); rerr != nil && first == nil {
			first = rerr
		}
		return nil
	})
	if err != nil && first == nil {
		first = err
	}
	return first
}

// Recover applies one journal.
func Recover(journalPath string) error {
	b, err := os.ReadFile(journalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var j Journal
	if err := json.Unmarshal(b, &j); err != nil {
		return fmt.Errorf("journal %s: %w", journalPath, err)
	}
	switch j.Phase {
	case phaseStaged:
		_ = os.Remove(j.Stage)
		return os.Remove(journalPath)
	case phaseMoved:
		_, origErr := os.Stat(j.Original)
		_, stageErr := os.Stat(j.Stage)
		switch {
		case errors.Is(origErr, os.ErrNotExist) && stageErr == nil:
			if err := os.Rename(j.Stage, j.Original); err != nil {
				return err
			}
			_ = os.Remove(j.Backup)
			return os.Remove(journalPath)
		case errors.Is(origErr, os.ErrNotExist) && errors.Is(stageErr, os.ErrNotExist):
			if err := os.Rename(j.Backup, j.Original); err != nil {
				return err
			}
			return os.Remove(journalPath)
		default:
			// The encode is already at the original path.
			_ = os.Remove(j.Stage)
			_ = os.Remove(j.Backup)
			return os.Remove(journalPath)
		}
	case phaseDone:
		_ = os.Remove(j.Backup)
		_ = os.Remove(j.Stage)
		return os.Remove(journalPath)
	default:
		return fmt.Errorf("unknown journal phase %q", j.Phase)
	}
}

func place(src, dst string) error {
	if same, err := sameDevice(src, filepath.Dir(dst)); err == nil && same {
		_ = os.Remove(dst)
		if err := os.Rename(src, dst); err == nil {
			return nil
		}
	}
	return copySync(src, dst)
}

func copySync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".partial"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(dst))
	return nil
}

func writeJournal(path string, j Journal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	tmp := path + ".partial"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}
