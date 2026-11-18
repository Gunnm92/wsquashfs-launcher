package savesync

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncArchiveVideRefusee : une archive sans aucun fichier est refusée avant toute modification :
// le périmètre local reste intact (sans cette garde, elle l'effacerait).
func TestSyncArchiveVideRefusee(t *testing.T) {
	users := filepath.Join(t.TempDir(), "users")
	syncTestPopulate(t, users, "steamuser")
	before := saveFingerprint(users, CollectSaveFiles(users))
	var buf bytes.Buffer
	if _, err := writeSaveArchive(&buf, users, nil); err != nil {
		t.Fatal(err)
	}
	err := applySaveArchive(bytes.NewReader(buf.Bytes()), users, "deck")
	if err == nil || !strings.Contains(err.Error(), "archive vide") {
		t.Fatalf("archive vide : erreur %v, attendue « archive vide »", err)
	}
	if got := saveFingerprint(users, CollectSaveFiles(users)); got != before {
		t.Error("périmètre local modifié par une archive vide")
	}
}

// TestSyncArchiveTropVolumineuse : une archive dont le contenu décompressé dépasse la limite est
// refusée avant toute modification ; le dossier temporaire est retiré. Sous la limite, elle passe.
func TestSyncArchiveTropVolumineuse(t *testing.T) {
	users := filepath.Join(t.TempDir(), "users")
	syncTestPopulate(t, users, "steamuser")
	oldMax := syncMaxUnpacked
	t.Cleanup(func() { syncMaxUnpacked = oldMax })
	syncMaxUnpacked = 1000
	local := filepath.Join(t.TempDir(), "users")
	syncTestPut(t, filepath.Join(local, "deck", "Documents", "Game", "gros.sav"), strings.Repeat("x", 2000), syncTestTime)
	var buf bytes.Buffer
	if _, err := writeSaveArchive(&buf, local, CollectSaveFiles(local)); err != nil {
		t.Fatal(err)
	}
	before := saveFingerprint(users, CollectSaveFiles(users))
	err := applySaveArchive(bytes.NewReader(buf.Bytes()), users, "deck")
	if err == nil || !strings.Contains(err.Error(), "trop volumineuse") {
		t.Fatalf("archive au-delà du plafond : erreur %v", err)
	}
	if got := saveFingerprint(users, CollectSaveFiles(users)); got != before {
		t.Error("périmètre local modifié par une archive refusée")
	}
	entries, err := os.ReadDir(filepath.Dir(users))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".wsq-sync-") {
			t.Errorf("dossier temporaire gardé : %s", e.Name())
		}
	}
	syncMaxUnpacked = 10000
	if err := applySaveArchive(bytes.NewReader(buf.Bytes()), users, "deck"); err != nil {
		t.Errorf("archive sous le plafond refusée : %v", err)
	}
}
