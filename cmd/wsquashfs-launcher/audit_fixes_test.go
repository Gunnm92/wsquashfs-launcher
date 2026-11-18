package main

// Tests des correctifs de l'audit du 16/11 : archives de synchro hors de /tmp et bornées, droits des
// fichiers reçus, maintenance interrompue par SIGTERM, image de --clean démontée sur signal, tar et
// membre commençant par un tiret, SAVEDIR/SAVEFILES hors du prefix.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncFetchRefusesHugeArchive : une archive reçue plus grosse que la limite est refusée, rien
// n'est modifié ici et le fichier temporaire est retiré.
func TestSyncFetchRefusesHugeArchive(t *testing.T) {
	oldMax := syncMaxBody
	t.Cleanup(func() { syncMaxBody = oldMax })
	syncMaxBody = 64
	r := newSyncRig(t)
	local := clientUsers(t)
	_, err := r.client.Fetch(syncer().GameTarget(syncRigGame, local))
	if err == nil || !strings.Contains(err.Error(), "trop volumineuse") {
		t.Fatalf("archive trop grosse acceptée : %v", err)
	}
	if pathExists(local) {
		t.Error("sauvegardes locales modifiées malgré le refus")
	}
	if left, _ := filepath.Glob(filepath.Join(syncer().TempDir(), "wsq-recu-*")); len(left) != 0 {
		t.Errorf("fichier temporaire laissé : %v", left)
	}
}

// TestArchiveUsersDashMember : un dossier dont le nom commence par un tiret s'archive (il n'est pas
// lu comme une option de tar).
func TestArchiveUsersDashMember(t *testing.T) {
	t.Setenv("WSQUASHFS_BACKUP_DIR", t.TempDir())
	parent := t.TempDir()
	writeTree(t, filepath.Join(parent, "-jeu"), map[string]string{"slot.sav": "partie"})
	out, err := archiveUsers("jeu", parent, "-jeu")
	if err != nil {
		t.Fatalf("archive impossible : %v", err)
	}
	list, err := exec.Command("tar", "-tf", out).Output()
	if err != nil || !strings.Contains(string(list), "-jeu/slot.sav") {
		t.Errorf("contenu de l'archive : %q, %v", list, err)
	}
}

// TestSavedirOutsidePrefixIgnored : SAVEDIR ou SAVEFILES qui sortent du prefix (« .. », chemin
// absolu) sont ignorés : le dossier ou le fichier de la machine reste en place, rien n'est copié.
func TestSavedirOutsidePrefixIgnored(t *testing.T) {
	capteUI(t)
	root := t.TempDir()
	isolateState(t)
	launch.winePoint, launch.gameName = filepath.Join(root, "prefix"), "Jeu"
	ludo := filepath.Join(root, "ludo")
	t.Setenv("WSQUASHFS_BATOCERA_SAVES", ludo)
	outside := filepath.Join(root, "dehors")
	writeTree(t, outside, map[string]string{"cle": "PRIVEE"})
	if err := os.MkdirAll(launch.winePoint, 0o755); err != nil {
		t.Fatal(err)
	}
	check := func(what string) {
		t.Helper()
		if st, err := os.Lstat(outside); err != nil || !st.IsDir() {
			t.Fatalf("%s : dossier hors du prefix remplacé ou retiré (%v)", what, err)
		}
		if st, err := os.Lstat(filepath.Join(outside, "cle")); err != nil || !st.Mode().IsRegular() {
			t.Errorf("%s : fichier hors du prefix déplacé (%v)", what, err)
		}
		if pathExists(filepath.Join(ludo, "Jeu", "cle")) {
			t.Errorf("%s : fichier hors du prefix copié dans la ludothèque", what)
		}
	}
	applySavedir(`..\dehors`, "")
	check("SAVEDIR relatif")
	applySavedir(outside, "")
	check("SAVEDIR absolu")
	applySavedir("", `../dehors/cle;drive_c/ok.dat`)
	check("SAVEFILES")
	// L'entrée valide de la même liste est traitée.
	if link, err := os.Readlink(filepath.Join(launch.winePoint, "drive_c", "ok.dat")); err != nil || link != filepath.Join(ludo, "Jeu", "drive_c", "ok.dat") {
		t.Errorf("SAVEFILES valide non relié : %q, %v", link, err)
	}
}

// TestCleanupRemovesPurgeMount : --clean interrompu pendant la recherche de doublons : cleanup
// retire le dossier de l'image montée (démontée d'abord si elle l'est encore).
func TestCleanupRemovesPurgeMount(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd); purgeMount.Store(nil) }) // cleanup se place dans /
	mnt := filepath.Join(t.TempDir(), "purge.123")
	if err := os.Mkdir(mnt, 0o755); err != nil {
		t.Fatal(err)
	}
	purgeMount.Store(&mnt)
	cleanup()
	if pathExists(mnt) {
		t.Error("dossier de l'image de --clean laissé après cleanup")
	}
}
