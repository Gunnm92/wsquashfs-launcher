package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestListFindsImagesNotRecorded : --list retrouve l'image d'un jeu dont rien n'est mémorisé
// (sauvegardes reçues par --sync) ou dont l'image a changé de système ; sinon il dit « introuvable »
// et propose l'image au nom proche (« (demul) » ajouté, .squashfs d'un autre émulateur).
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
	for _, g := range []string{"Dispatch", "Moved", "After Dark", "Chase H.Q. 2", "Disparu"} {
		writeTree(t, filepath.Join(savesDir, g, "drive_c", "users", "steamuser", "Documents"), map[string]string{"s.sav": g})
	}
	record := func(game, path string) {
		writeTree(t, filepath.Join(savesDir, ".images"), map[string]string{game: path + "\n"})
	}
	record("Moved", filepath.Join(roms, "segare", "Moved.wsquashfs"))         // déplacée vers win/
	record("After Dark", filepath.Join(roms, "unis", "After Dark.wsquashfs")) // renommée « (demul) »
	record("Chase H.Q. 2", filepath.Join(roms, "type-x", "Chase H.Q. 2.wsquashfs"))
	record("Disparu", filepath.Join(roms, "win", "Disparu.wsquashfs"))

	if got := gameImage("Dispatch"); got != filepath.Join(roms, "win", "Dispatch.wsquashfs") {
		t.Errorf("Dispatch (rien de mémorisé) : %q", got)
	}
	if got := gameImage("Moved"); got != filepath.Join(roms, "win", "Moved.wsquashfs") {
		t.Errorf("Moved (déplacée) : %q", got)
	}
	capture, _ := capteUI(t)
	cmdList()
	out := capture.String()
	for _, want := range []string{
		filepath.Join(roms, "win", "Dispatch.wsquashfs"),
		"introuvable : " + filepath.Join(roms, "unis", "After Dark.wsquashfs") + " (proche : " + filepath.Join(roms, "unis", "After Dark (demul).wsquashfs") + ")",
		"(proche : " + filepath.Join(roms, "type-x2", "Chase H.Q. 2.squashfs") + ")",
		"introuvable : " + filepath.Join(roms, "win", "Disparu.wsquashfs"),
	} {
		if !strings.Contains(strings.Join(strings.Fields(out), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("--list sans %q :\n%s", want, out)
		}
	}
	if strings.Contains(out, "inconnue") {
		t.Errorf("« inconnue » alors que l'image est là :\n%s", out)
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
