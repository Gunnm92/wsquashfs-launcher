package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestListFindsImagesNotRecorded : --list retrouve l'image d'un jeu dont rien n'est mémorisé
// (sauvegardes reçues par --sync) ou dont l'image a changé de système. Un jeu sans image (renommée
// ou retirée) n'a pas de ligne : un rappel en fin de liste propose --clean (rien d'écrit) ou de
// réinstaller le jeu (sauvegardes, avec l'image au nom proche).
func TestListFindsImagesNotRecorded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	isolateSaves(t)
	romsIndexDone = nil
	t.Cleanup(func() { romsIndexDone = nil })
	roms := filepath.Join(home, "roms")
	writeTree(t, roms, map[string]string{
		"win/Dispatch.wsquashfs":            "hsqs",
		"win/Moved.wsquashfs":               "hsqs",
		"unis/After Dark (demul).wsquashfs": "hsqs",
		"type-x2/Chase H.Q. 2.squashfs":     "hsqs",
	})
	if err := saveAgentConfig(agentConfig{Port: 8790, Roms: []string{roms}}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{"Dispatch", "Moved", "Chase H.Q. 2"} {
		writeTree(t, filepath.Join(savesDir, g, "drive_c", "users", "steamuser", "Documents"), map[string]string{"s.sav": g})
	}
	shellOnly(t, "After Dark") // sauvegardes supprimées : marqueurs d'overlay seulement
	record := func(game, path string) {
		writeTree(t, filepath.Join(savesDir, ".images"), map[string]string{game: path + "\n"})
	}
	record("Moved", filepath.Join(roms, "segare", "Moved.wsquashfs"))
	record("After Dark", filepath.Join(roms, "unis", "After Dark.wsquashfs"))
	record("Chase H.Q. 2", filepath.Join(roms, "type-x", "Chase H.Q. 2.wsquashfs"))

	if got := gameImage("Dispatch"); got != filepath.Join(roms, "win", "Dispatch.wsquashfs") {
		t.Errorf("Dispatch (rien de mémorisé) : %q", got)
	}
	if got := gameImage("Moved"); got != filepath.Join(roms, "win", "Moved.wsquashfs") {
		t.Errorf("Moved (déplacée) : %q", got)
	}
	capture, _ := capteUI(t)
	cmdList()
	out := strings.Join(strings.Fields(capture.String()), " ")
	for _, want := range []string{
		"2 jeux",
		filepath.Join(roms, "win", "Dispatch.wsquashfs"),
		filepath.Join(roms, "win", "Moved.wsquashfs"),
		"1 jeu sans image ni sauvegarde, non listé(s) : wsquashfs-launcher --clean les retire.",
		"1 jeu sans image mais avec des sauvegardes, non listé(s) : réinstaller le jeu",
		"Chase H.Q. 2 (image proche : " + filepath.Join(roms, "type-x2", "Chase H.Q. 2.squashfs") + ")",
	} {
		if !strings.Contains(out, strings.Join(strings.Fields(want), " ")) {
			t.Errorf("--list sans %q :\n%s", want, capture.String())
		}
	}
	for _, unwanted := range []string{"inconnue", "introuvable", "│ After Dark"} {
		if strings.Contains(capture.String(), unwanted) {
			t.Errorf("--list contient %q :\n%s", unwanted, capture.String())
		}
	}
}

// shellOnly : sauvegardes réduites à des marqueurs d'overlay (comme après « Supprimer… » du manager).
func shellOnly(t *testing.T, game string) {
	t.Helper()
	users := filepath.Join(savesDir, game, "drive_c", "users", "arcade")
	writeTree(t, users, map[string]string{
		".wh..wh..opq":           "",
		"AppData/.wh..wh..opq":   "",
		"Documents/.wh..wh..opq": "",
	})
	// Liens que Wine pose vers HOME : ils ne contiennent rien
	if err := os.Symlink(os.TempDir(), filepath.Join(users, "Desktop")); err != nil {
		t.Fatal(err)
	}
}

// TestCleanPrunesOrphanSaves : --clean retire un jeu sans image et sans sauvegarde (overlay réduit à
// des marqueurs, prefix seul, ou seul le chemin mémorisé) ; il garde un jeu dont l'image existe, même
// vide, et un jeu sans image qui a encore des sauvegardes (profil Public compris).
func TestCleanPrunesOrphanSaves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	isolateSaves(t)
	romsIndexDone = nil
	t.Cleanup(func() { romsIndexDone = nil })
	roms := filepath.Join(home, "roms")
	writeTree(t, roms, map[string]string{"win/Present.wsquashfs": "hsqs"})
	record := func(game, path string) {
		writeTree(t, filepath.Join(savesDir, ".images"), map[string]string{game: path + "\n"})
	}
	shellOnly(t, "After Dark")
	record("After Dark", filepath.Join(roms, "unis", "After Dark.wsquashfs"))
	shellOnly(t, "Present")
	record("Present", filepath.Join(roms, "win", "Present.wsquashfs"))
	writeTree(t, filepath.Join(savesDir, "Gardé", "drive_c", "users", "arcade", "Documents"), map[string]string{"slot.sav": "x"})
	record("Gardé", filepath.Join(roms, "win", "Gardé.wsquashfs"))
	// Sauvegarde dans le profil Public (écrite là par certains jeux) : gardé
	writeTree(t, filepath.Join(savesDir, "Public", "drive_c", "users", "Public", "Documents", "EMPRESS"), map[string]string{"dbdata.bin": "x"})
	record("Public", filepath.Join(roms, "win", "Public.wsquashfs"))
	// Prefix seul (pas de sauvegarde) et fichier temporaire du profil : retiré
	writeTree(t, filepath.Join(savesDir, "Prefix seul"), map[string]string{
		"config_info": "proton", "drive_c/windows/system32/x.dll": "dll", "drive_c/users/root/Temp/t.tmp": "tmp",
		"drive_c/users/steamuser/AppData/Local/NVIDIA/DXCache/c.bin": "cache"})
	record("Prefix seul", filepath.Join(roms, "win", "Prefix seul.wsquashfs"))
	record("Chemin seul", filepath.Join(roms, "type-x", "Chemin seul.wsquashfs"))

	if got := strings.Join(pruneOrphanSaves(), ","); got != "After Dark,Chemin seul,Prefix seul" {
		t.Errorf("retirés : %q", got)
	}
	for _, g := range []string{"After Dark", "Chemin seul", "Prefix seul"} {
		if pathExists(filepath.Join(savesDir, g)) || pathExists(filepath.Join(savesDir, ".images", g)) {
			t.Errorf("%s : restes encore là", g)
		}
	}
	if strings.Join(knownGames(), ",") != "Gardé,Present,Public" {
		t.Errorf("jeux restants : %v", knownGames())
	}
}

// TestEraseForgetsTheGame : --erase efface l'overlay d'un jeu (ex. celui d'une image renommée depuis)
// et son chemin d'image mémorisé : --list ne le montre plus.
func TestEraseForgetsTheGame(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	isolateSaves(t)
	writeTree(t, filepath.Join(savesDir, "After Dark", "drive_c", "users", "arcade"), map[string]string{".wh..wh..opq": ""})
	writeTree(t, filepath.Join(savesDir, ".images"), map[string]string{"After Dark": "/roms/unis/After Dark.wsquashfs\n"})
	capteUI(t)
	eraseSaves("After Dark", newUIGames([]string{"After Dark"}))
	if pathExists(filepath.Join(savesDir, "After Dark")) || pathExists(filepath.Join(savesDir, ".images", "After Dark")) {
		t.Error("overlay ou chemin mémorisé encore là")
	}
	if len(knownGames()) != 0 {
		t.Errorf("--list : %v", knownGames())
	}
}
