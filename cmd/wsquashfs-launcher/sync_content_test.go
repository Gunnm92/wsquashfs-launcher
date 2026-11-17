package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestContentFingerprint : l'empreinte de contenu ignore les dates, change avec le contenu, et le
// cache des hash suit la taille et la date des fichiers.
func TestContentFingerprint(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"slot1.sav": "aaa", "opt/o.ini": "x=1"})
	tg := syncTarget{Dir: dir, Plain: true}
	base := tg.content(tg.files())
	if base == "" {
		t.Fatal("empreinte vide")
	}
	// Seule la date change : contenu identique, empreinte habituelle différente.
	fpBefore := tg.fingerprint(tg.files())
	mt := time.Unix(1_800_000_000, 0)
	if err := os.Chtimes(filepath.Join(dir, "slot1.sav"), mt, mt); err != nil {
		t.Fatal(err)
	}
	if got := tg.content(tg.files()); got != base {
		t.Errorf("la date seule ne doit pas changer l'empreinte de contenu")
	}
	if tg.fingerprint(tg.files()) == fpBefore {
		t.Errorf("l'empreinte habituelle devait changer avec la date")
	}
	// Contenu changé à taille égale, date remise : le cache (taille, date) ne le voit pas, mais
	// avec une autre date il est relu.
	p := filepath.Join(dir, "slot1.sav")
	if err := os.WriteFile(p, []byte("bbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	mt2 := time.Unix(1_800_000_100, 0)
	_ = os.Chtimes(p, mt2, mt2)
	changed := tg.content(tg.files())
	if changed == base {
		t.Errorf("contenu changé : l'empreinte devait changer (date nouvelle)")
	}
	// Taille changée, même date : cache invalidé.
	if err := os.WriteFile(p, []byte("bbbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(p, mt2, mt2)
	if got := tg.content(tg.files()); got == changed || got == "" {
		t.Errorf("taille changée : cache non invalidé")
	}
	// Fichier illisible (jamais lu, donc absent du cache) : "".
	writeTree(t, dir, map[string]string{"neuf.sav": "n"})
	files := tg.files()
	if err := os.Remove(filepath.Join(dir, "neuf.sav")); err != nil {
		t.Fatal(err)
	}
	if got := tg.content(files); got != "" {
		t.Errorf("fichier illisible : %q, attendu \"\"", got)
	}
	if tg.content(nil) != "" {
		t.Errorf("sans fichier : attendu \"\"")
	}
}

// TestContentFingerprintGameProfile : deux jeux de même contenu sous des profils Wine différents
// ont la même empreinte de contenu.
func TestContentFingerprintGameProfile(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeTree(t, a, map[string]string{"alice/Documents/s.dat": "zzz"})
	writeTree(t, b, map[string]string{"steamuser/Documents/s.dat": "zzz"})
	fa := contentFingerprint(a, collectSaveFiles(a), true)
	fb := contentFingerprint(b, collectSaveFiles(b), true)
	if fa == "" || fa != fb {
		t.Errorf("empreintes : %q et %q", fa, fb)
	}
}

// TestContentCacheBounded : le cache est vidé au-delà de sa taille maximale.
func TestContentCacheBounded(t *testing.T) {
	contentCacheMu.Lock()
	for i := 0; i < contentCacheMax; i++ {
		contentCache[contentKey{path: "x", size: int64(i)}] = "h"
	}
	contentCacheMu.Unlock()
	f := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(f, []byte("a"), 0o644)
	if _, ok := fileContentHash(f, 1, 1); !ok {
		t.Fatal("hash impossible")
	}
	contentCacheMu.Lock()
	n := len(contentCache)
	contentCacheMu.Unlock()
	if n > 1 {
		t.Errorf("cache non vidé : %d entrées", n)
	}
}

// TestDecideSyncContent : contenus égaux → à jour malgré des empreintes différentes ; contenu
// distant inconnu → comportement inchangé.
func TestDecideSyncContent(t *testing.T) {
	if got := decideSync("a2", "a", 10, "b2", "b", 20, "c", "c"); got != syncUpToDate {
		t.Errorf("contenus égaux : %v", got)
	}
	if got := decideSync("a2", "a", 10, "b2", "b", 20, "c", ""); got != syncConflict {
		t.Errorf("contenu distant inconnu : %v", got)
	}
	if got := decideSync("a2", "a", 10, "b2", "b", 20, "c", "d"); got != syncConflict {
		t.Errorf("contenus différents : %v", got)
	}
}
