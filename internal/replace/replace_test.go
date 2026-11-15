package replace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommitReplacesAndRemovesBackup(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "movie.mkv")
	enc := filepath.Join(dir, "encoded.mkv")
	if err := os.WriteFile(orig, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(enc, []byte("smaller-av1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Commit(orig, enc); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "smaller-av1" {
		t.Fatalf("original path has %q", got)
	}
	for _, suffix := range []string{".transarc.bak", ".transarc.stage", ".transarc.journal"} {
		if _, err := os.Stat(orig + suffix); !os.IsNotExist(err) {
			t.Fatalf("leftover %s", suffix)
		}
	}
}

func TestRecoverStagedLeavesOriginal(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(orig, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := orig + ".transarc.stage"
	if err := os.WriteFile(stage, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := Journal{Phase: phaseStaged, Original: orig, Stage: stage, Backup: orig + ".transarc.bak"}
	if err := writeJournal(orig+".transarc.journal", j); err != nil {
		t.Fatal(err)
	}
	if err := RecoverTree(dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(orig)
	if string(got) != "original" {
		t.Fatalf("original changed to %q", got)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("stage survived recovery")
	}
}

func TestRecoverMovedFinishesSwap(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "movie.mkv")
	stage := orig + ".transarc.stage"
	bak := orig + ".transarc.bak"
	if err := os.WriteFile(bak, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stage, []byte("encoded"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := Journal{Phase: phaseMoved, Original: orig, Stage: stage, Backup: bak}
	if err := writeJournal(orig+".transarc.journal", j); err != nil {
		t.Fatal(err)
	}
	if err := Recover(orig + ".transarc.journal"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "encoded" {
		t.Fatalf("got %q", got)
	}
}
