package main

import (
	"path/filepath"
	"strings"
	"testing"

	"wsquashfs-launcher/internal/savesync"
)

// completionRig : HOME temporaire avec des jeux sauvegardés, une machine associée (--sync), un
// dossier déclaré et une machine associée à l'agent.
func completionRig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("WSQUASHFS_EDEN_DIR", "")
	isolateSaves(t)
	for _, g := range []string{"Hollow Knight", "Castlevania - Belmont's Curse", "Tekken 8"} {
		writeTree(t, filepath.Join(conf.savesDir, g, "drive_c", "users", "steamuser", "Documents"), map[string]string{"s.sav": g})
	}
	writeTree(t, filepath.Join(home, ".local", "share", "hookshot", "save"), map[string]string{"slot": "x"})
	cfg := savesync.Config{ID: "0123456789abcdef0123456789abcdef", Peers: []savesync.Peer{{Name: "aibox"}, {Name: "steamdeck"}}}
	cfg, err := savesync.AddDir(cfg, "hookshot", ".local/share/hookshot/save")
	if err != nil {
		t.Fatal(err)
	}
	if err := savesync.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := saveAgentConfig(agentConfig{Port: 8790, Machines: []agentMachine{{ID: "m1", Name: "wsquashfs-manager"}}}); err != nil {
		t.Fatal(err)
	}
}

// TestCompleteLine : ce que reçoit bash pour chaque début de ligne (remplacements du mot courant).
func TestCompleteLine(t *testing.T) {
	completionRig(t)
	cases := map[string]string{
		"wsquashfs-launcher --sy":                     "--sync",
		"wsquashfs-launcher --sync hol":               `Hollow\ Knight`, // sans casse, espace échappé
		"wsquashfs-launcher --sync Cast":              `Castlevania\ -\ Belmont\'s\ Curse`,
		`wsquashfs-launcher --sync "Cast`:             "Castlevania - Belmont's Curse", // guillemet ouvert : tel quel
		`wsquashfs-launcher --sync Hollow\ K`:         `Hollow\ Knight`,
		"wsquashfs-launcher --sync dossier:ho":        "hookshot", // bash coupe le mot au « : »
		"wsquashfs-launcher --sync st":                "status",
		"wsquashfs-launcher --sync status T":          "Tekken\\ 8",
		"wsquashfs-launcher --sync forget ":           "aibox|steamdeck",
		"wsquashfs-launcher --sync dir ":              "add|forget",
		"wsquashfs-launcher --sync dir forget ":       "hookshot",
		"wsquashfs-launcher --save T":                 `Tekken\ 8`,
		"wsquashfs-launcher --erase ":                 `Castlevania\ -\ Belmont\'s\ Curse|Hollow\ Knight|Tekken\ 8`,
		"wsquashfs-launcher --pair ":                  "forget|port|reset|roms",
		"wsquashfs-launcher --pair forget ":           "wsquashfs-manager",
		"wsquashfs-launcher --pair roms ":             "", // un dossier : noms de fichiers
		"wsquashfs-launcher --agent ":                 "run|status|stop",
		"wsquashfs-launcher --agent run ":             "log",
		"wsquashfs-launcher --doctor ":                "json",
		"wsquashfs-launcher --tray ":                  "off|on|stop",
		"wsquashfs-launcher --wine ":                  "system",
		"wsquashfs-launcher ":                         "", // un jeu à lancer : noms de fichiers
		"wsquashfs-launcher --info ":                  "",
		"wsquashfs-launcher --show Jeu.wsquashfs --s": "--set",
	}
	for line, want := range cases {
		if got := strings.Join(completeLine(line), "|"); got != want {
			t.Errorf("%q : %q, attendu %q", line, got, want)
		}
	}
}

// TestCompleteCursorPosition : seule la partie avant le curseur compte (COMP_POINT, en caractères).
func TestCompleteCursorPosition(t *testing.T) {
	completionRig(t)
	out := captureStdout(t, func() { cmdComplete([]string{"29", "wsquashfs-launcher --sync hol --pair"}) })
	if out != "Hollow\\ Knight\n" {
		t.Errorf("sortie : %q", out)
	}
}
