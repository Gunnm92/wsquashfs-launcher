package savesync

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// TestSyncArchiveKeepsFileMode : les droits d'un fichier passent par l'archive (600 reste 600, un
// exécutable le reste) au lieu de devenir 644.
func TestSyncArchiveKeepsFileMode(t *testing.T) {
	src := filepath.Join(t.TempDir(), "users")
	docs := filepath.Join(src, "deck", "Documents")
	writeTree(t, docs, map[string]string{"prive.sav": "secret", "outil.sh": "#!/bin/sh\n", "normal.sav": "x"})
	for name, mode := range map[string]os.FileMode{"prive.sav": 0o600, "outil.sh": 0o755, "normal.sav": 0o644} {
		if err := os.Chmod(filepath.Join(docs, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if _, err := writeSaveArchive(&buf, src, CollectSaveFiles(src)); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "users")
	if err := applySaveArchive(&buf, dst, "deck"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{"prive.sav": 0o600, "outil.sh": 0o755, "normal.sav": 0o644} {
		st, err := os.Stat(filepath.Join(dst, "deck", "Documents", name))
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Errorf("%s : droits %o, attendus %o", name, got, want)
		}
	}
}

// TestSyncTempFileInCache : les archives temporaires de synchro vont dans le cache du lanceur, pas
// dans /tmp ; --clean (PruneTemp) retire les restes anciens et garde une archive récente.
func TestSyncTempFileInCache(t *testing.T) {
	s := testSyncer(t)
	f, err := s.TempFile("wsq-sync-*.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if filepath.Dir(f.Name()) != filepath.Join(s.env.CacheDir, "tmp") {
		t.Fatalf("archive temporaire dans %s, attendue dans %s", filepath.Dir(f.Name()), filepath.Join(s.env.CacheDir, "tmp"))
	}
	old := filepath.Join(s.TempDir(), "wsq-recu-reste.tar.gz")
	if err := os.WriteFile(old, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * TempMaxAge)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if freed := s.PruneTemp(); freed != 5 {
		t.Errorf("place libérée : %d, attendu 5", freed)
	}
	if fileExists(old) {
		t.Error("reste ancien gardé")
	}
	if !fileExists(f.Name()) {
		t.Error("archive récente (synchro en cours) retirée")
	}
}
