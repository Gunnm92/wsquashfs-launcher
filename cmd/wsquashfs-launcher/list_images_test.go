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
