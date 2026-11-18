package main

// Tests du dossier de jeu sans autorun.cmd : exclusions, choix de l'exécutable,
// mémo <dossier>.autorun et résolution du chemin (comme un dossier .pc).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// detectIsolate : réglages isolés, configuration utilisateur vide.
func detectIsolate(t *testing.T) {
	t.Helper()
	isolateState(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// candidatsRel : chemins relatifs des candidats.
func candidatsRel(cands []exeCandidat) []string {
	var out []string
	for _, c := range cands {
		out = append(out, c.rel)
	}
	return out
}

// TestDetectExcludesInstallers : désinstalleurs, redistribuables, anti-crash et
// dossiers d'installeurs ne sont pas des candidats ; le jeu, si.
func TestDetectExcludesInstallers(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"Jeu.exe":                                          "jeu",
		"unins000.exe":                                     "désinstalleur",
		"vcredist_x64.exe":                                 "redist",
		"UnityCrashHandler64.exe":                          "crash",
		"_CommonRedist/x.exe":                              "redist",
		"Engine/Binaries/Win64/Tool.exe":                   "moteur",
		"drive_c/windows/system32/cmd.exe":                 "système",
		"CrashSender1404.exe":                              "crash",
		"Jeu.app/Contents/Data/server/DedicatedServer.exe": "macOS",
		"Server/DedicatedServer.exe":                       "serveur",
		"AuraSDK/AuraSdk.exe":                              "rétroéclairage",
	})
	cands, exclus := detectExes(root)
	if got := candidatsRel(cands); len(got) != 1 || got[0] != "Jeu.exe" {
		t.Errorf("candidats = %v, attendu [Jeu.exe]", got)
	}
	if len(exclus) != 10 {
		t.Errorf("exclus = %v, attendu 10 entrées", exclus)
	}
}

// TestDetectCaseInsensitiveExe : extension .EXE reconnue, liens non suivis.
func TestDetectCaseInsensitiveExe(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	writeTree(t, root, map[string]string{"GAME.EXE": "jeu", "notes.txt": "x"})
	writeTree(t, other, map[string]string{"Lien.exe": "ailleurs"})
	if err := os.Symlink(other, filepath.Join(root, "lien")); err != nil {
		t.Skip("liens symboliques indisponibles : " + err.Error())
	}
	cands, _ := detectExes(root)
	if got := candidatsRel(cands); len(got) != 1 || got[0] != "GAME.EXE" {
		t.Errorf("candidats = %v, attendu [GAME.EXE] (lien de dossier non suivi)", got)
	}
}

// TestDetectSingleCandidateWritesNoMemo : un seul candidat, rien n'est écrit,
// l'exécutable passe par la couche « détection ».
func TestDetectSingleCandidateWritesNoMemo(t *testing.T) {
	detectIsolate(t)
	parent := t.TempDir()
	dossier := filepath.Join(parent, "Jeu")
	writeTree(t, dossier, map[string]string{"unins000.exe": "x", "Jeu.exe": "jeu"})
	launch.wsquashfsFile = dossier
	launch.folderOverlay = true
	launch.autorunOverride = ""

	ensureFolderCmd()

	if _, err := os.Stat(dossier + ".autorun"); !os.IsNotExist(err) {
		t.Errorf("mémo écrit alors qu'un seul candidat existe : %v", err)
	}
	loadLayers("")
	if cmd := autorunVar("CMD", ""); cmd != "Jeu.exe" {
		t.Errorf("CMD = %q, attendu Jeu.exe", cmd)
	}
}

// TestDetectRootCandidateWins : un seul .exe à la racine n'est un choix sûr
// (sans mémo) que si son nom ressemble au jeu, ou s'il lance un jeu Unreal ;
// sinon mémo (Cyberpunk 2077 : REDprelauncher.exe à la racine, 18/11).
func TestDetectRootCandidateWins(t *testing.T) {
	cases := []struct {
		name, dossier string
		tree          map[string]string
		memo          bool
		cmd           string
	}{
		{"nom proche", "Jeu", map[string]string{"bin/Moteur.exe": "moteur", "Jeu.exe": "jeu"}, false, "Jeu.exe"},
		{"lanceur Unreal", "Le petit page", map[string]string{
			"Storybook.exe": "lanceur",
			"Storybook/Binaries/Win64/Storybook-Win64-Shipping.exe": strings.Repeat("x", 5000),
		}, false, "Storybook.exe"},
		{"prélanceur sans rapport", "Cyberpunk 2077", map[string]string{
			"REDprelauncher.exe":        "prélanceur",
			"bin/x64/Cyberpunk2077.exe": "jeu",
		}, true, "Cyberpunk2077.exe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			detectIsolate(t)
			dossier := filepath.Join(t.TempDir(), c.dossier)
			writeTree(t, dossier, c.tree)
			launch.wsquashfsFile = dossier
			launch.folderOverlay = true

			ensureFolderCmd()

			_, err := os.Stat(dossier + ".autorun")
			if memo := err == nil; memo != c.memo {
				t.Errorf("mémo écrit = %v, attendu %v", memo, c.memo)
			}
			loadLayers("")
			if cmd := autorunVar("CMD", ""); cmd != c.cmd {
				t.Errorf("CMD = %q, attendu %q", cmd, c.cmd)
			}
		})
	}
}

// TestDetectMemoNearestName : plusieurs candidats : le mémo porte le nom le
// plus proche du dossier, les autres en REM, fins de ligne CRLF.
func TestDetectMemoNearestName(t *testing.T) {
	detectIsolate(t)
	parent := t.TempDir()
	dossier := filepath.Join(parent, "MonJeu")
	writeTree(t, dossier, map[string]string{
		"bin/Autre.exe":   strings.Repeat("x", 5000),
		"sous/MonJeu.exe": "jeu",
	})
	launch.wsquashfsFile = dossier
	launch.folderOverlay = true

	ensureFolderCmd()

	memo := dossier + ".autorun"
	data, err := os.ReadFile(memo)
	if err != nil {
		t.Fatalf("mémo absent : %v", err)
	}
	text := string(data)
	for _, want := range []string{"REM Mémo du lanceur", "REM   bin/Autre.exe", "DIR=sous\r\n", "CMD=MonJeu.exe\r\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("mémo sans %q :\n%s", want, text)
		}
	}
	if strings.Count(text, "\n") != strings.Count(text, "\r\n") {
		t.Errorf("fins de ligne non CRLF :\n%q", text)
	}
	if launch.autorunOverride != memo {
		t.Errorf("autorunOverride = %q, attendu %q", launch.autorunOverride, memo)
	}
	loadLayers("")
	if cmd := autorunVar("CMD", ""); cmd != "MonJeu.exe" {
		t.Errorf("CMD = %q, attendu MonJeu.exe", cmd)
	}
}

// TestDetectMemoFallsBackToBiggest : aucun nom proche : le plus gros fichier.
func TestDetectMemoFallsBackToBiggest(t *testing.T) {
	detectIsolate(t)
	dossier := filepath.Join(t.TempDir(), "Zz")
	writeTree(t, dossier, map[string]string{
		"abc.exe": "petit",
		"def.exe": strings.Repeat("g", 4000),
	})
	launch.wsquashfsFile = dossier
	launch.folderOverlay = true

	ensureFolderCmd()

	loadLayers("")
	if cmd := autorunVar("CMD", ""); cmd != "def.exe" {
		t.Errorf("CMD = %q, attendu le plus gros fichier def.exe", cmd)
	}
}

// TestDetectNoCandidate : seulement des exclus, aucun candidat (ensureFolderCmd
// sort alors avec die, qui ne peut pas être testé ici : os.Exit).
func TestDetectNoCandidate(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"unins000.exe": "x", "dxsetup.exe": "x"})
	cands, exclus := detectExes(root)
	if len(cands) != 0 || len(exclus) != 2 {
		t.Errorf("candidats = %v, exclus = %v ; attendu aucun candidat et 2 exclus", candidatsRel(cands), exclus)
	}
}

// TestDetectSkipsWhenCmdAlreadySet : une couche donne déjà CMD (--set) : rien
// n'est cherché ni écrit.
func TestDetectSkipsWhenCmdAlreadySet(t *testing.T) {
	detectIsolate(t)
	dossier := filepath.Join(t.TempDir(), "Jeu")
	writeTree(t, dossier, map[string]string{"A.exe": strings.Repeat("a", 10), "B.exe": strings.Repeat("b", 10)})
	launch.wsquashfsFile = dossier
	launch.folderOverlay = true
	launch.setArgs = []string{"CMD=A.exe"}

	ensureFolderCmd()

	if _, err := os.Stat(dossier + ".autorun"); !os.IsNotExist(err) {
		t.Errorf("mémo écrit alors que --set donne CMD : %v", err)
	}
	if launch.detectLayer != "" {
		t.Errorf("couche détection posée alors que --set donne CMD : %q", launch.detectLayer)
	}
}

// TestResolveGamePathFolderWithoutAutorun : dossier sans autorun.cmd et sans
// suffixe : traité comme un dossier .pc (overlay), sans sortie.
func TestResolveGamePathFolderWithoutAutorun(t *testing.T) {
	detectIsolate(t)
	dossier := filepath.Join(t.TempDir(), "Jeu")
	writeTree(t, dossier, map[string]string{"Jeu.exe": "jeu"})
	launch.gameArg = dossier

	resolveGamePath()

	if !launch.folderOverlay || launch.dirMode {
		t.Errorf("folderOverlay = %v, dirMode = %v ; attendu overlay sans mode installé", launch.folderOverlay, launch.dirMode)
	}
}
