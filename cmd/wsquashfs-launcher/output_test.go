package main

// Tests de l'affichage côté lanceur : aide (usage.txt) et sorties machine, qui
// ne doivent jamais être colorées. La charte elle-même est testée dans
// internal/ui.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"wsquashfs-launcher/internal/ui"
)

const esc = "\x1b"

// capteUI remplace les sorties ui par des tampons, rendus au nettoyage.
func capteUI(t *testing.T) (out, errs *bytes.Buffer) {
	t.Helper()
	out, errs = &bytes.Buffer{}, &bytes.Buffer{}
	t.Cleanup(ui.SetOutput(out, errs))
	return out, errs
}

// horsTerminal : aucune variable de couleur ni de terminal forcé ; t.Setenv
// restaure les valeurs d'origine au nettoyage.
func horsTerminal(t *testing.T) {
	t.Helper()
	for _, k := range []string{"NO_COLOR", "CLICOLOR", "CLICOLOR_FORCE", "TTY_FORCE", "TERM", "COLORTERM"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

// ── Aide ───────────────────────────────────────────────────────────────

// TestHelpIdentique : hors terminal, ui.Help rend usage.txt octet pour octet.
func TestHelpIdentique(t *testing.T) {
	horsTerminal(t)
	out, _ := capteUI(t)
	ui.Help(usageText)
	if got := out.String(); got != usageText {
		t.Errorf("Help :\n--- obtenu\n%s\n--- attendu\n%s", got, usageText)
	}
}

// TestHelpSansEspaceFin : usage.txt ne doit contenir aucun espace de fin de
// ligne (sinon Out les retire et la sortie diverge du fichier).
func TestHelpSansEspaceFin(t *testing.T) {
	for i, l := range strings.Split(usageText, "\n") {
		if strings.HasSuffix(l, " ") {
			t.Errorf("usage.txt ligne %d : espace en fin de ligne", i+1)
		}
	}
}

// ── Sorties machine : jamais colorées ──────────────────────────────────

// captureStdout exécute fn et renvoie ce qu'il écrit sur os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	fn()
	os.Stdout = old
	_ = w.Close()
	b := <-done
	_ = r.Close()
	return string(b)
}

// TestDoctorJSONSansCouleur : --doctor json reste du JSON pur, sans ESC,
// même avec CLICOLOR_FORCE=1.
func TestDoctorJSONSansCouleur(t *testing.T) {
	horsTerminal(t)
	t.Setenv("CLICOLOR_FORCE", "1")
	oldSec, oldRun := docSections, docRunners
	t.Cleanup(func() { docSections, docRunners = oldSec, oldRun })
	docSections, docRunners = nil, nil
	docSection("Montage des images")
	doctorOK("/dev/fuse accessible")
	doctorFail("squashfuse absent : montage de l'image")
	docSection("Wine / Proton")
	doctorOK("Wine par défaut : Proton (/opt/wine/bin/wine)")
	docRunner("GE-Proton9", "/opt/GE-Proton9")

	for _, fatal := range []bool{true, false} {
		got := captureStdout(t, func() { printDoctorJSON(fatal) })
		if strings.Contains(got, esc) {
			t.Errorf("fatal=%v : code ESC dans le JSON : %q", fatal, got)
		}
		if !json.Valid([]byte(got)) {
			t.Errorf("fatal=%v : JSON invalide : %q", fatal, got)
		}
		var rapport doctorJSON
		if err := json.Unmarshal([]byte(got), &rapport); err != nil {
			t.Fatalf("fatal=%v : décodage : %v", fatal, err)
		}
		if fatal && (rapport.Code != 1 || !strings.HasPrefix(rapport.Verdict, "✗")) {
			t.Errorf("fatal : code %d, verdict %q", rapport.Code, rapport.Verdict)
		}
		if !fatal && (rapport.Code != 0 || !strings.HasPrefix(rapport.Verdict, "✓")) {
			t.Errorf("non fatal : code %d, verdict %q", rapport.Code, rapport.Verdict)
		}
	}
}

// TestTrialJSONSansCouleur : le rapport --trial reste du JSON pur. Une image
// absente renvoie « noimage » sans rien lancer (« busy » si un lanceur tourne
// déjà sur la machine : rapport tout aussi pur).
func TestTrialJSONSansCouleur(t *testing.T) {
	horsTerminal(t)
	t.Setenv("CLICOLOR_FORCE", "1")
	var code int
	got := captureStdout(t, func() {
		code = runTrial(t.TempDir()+"/absent.wsquashfs", 1, "", "")
	})
	if strings.Contains(got, esc) {
		t.Errorf("code ESC dans le rapport --trial : %q", got)
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("JSON invalide : %q", got)
	}
	var rapport trialReport
	if err := json.Unmarshal([]byte(got), &rapport); err != nil {
		t.Fatalf("décodage : %v", err)
	}
	switch rapport.Error {
	case "noimage":
		if code != 4 {
			t.Errorf("noimage : code %d, attendu 4", code)
		}
	case "busy":
		if code != 3 {
			t.Errorf("busy : code %d, attendu 3", code)
		}
	default:
		t.Errorf("erreur %q, attendu noimage (ou busy)", rapport.Error)
	}
}
