package main

// Tests de la charte d'affichage (ui.go) et des sorties machine, qui ne doivent
// jamais être colorées. Les fonctions ui* écrivent dans uiStdout / uiStderr,
// remplacés ici par des bytes.Buffer (« hors terminal »).

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

const esc = "\x1b"

// capteUI remplace les sorties ui par des tampons, rendus au nettoyage.
func capteUI(t *testing.T) (out, errs *bytes.Buffer) {
	t.Helper()
	out, errs = &bytes.Buffer{}, &bytes.Buffer{}
	oldOut, oldErr := uiStdout, uiStderr
	uiStdout, uiStderr = out, errs
	t.Cleanup(func() { uiStdout, uiStderr = oldOut, oldErr })
	return out, errs
}

// fixeEnv positionne les variables de couleur ; "" = absente. t.Setenv
// restaure les valeurs d'origine au nettoyage.
func fixeEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for k, v := range vars {
		t.Setenv(k, v)
		if v == "" {
			_ = os.Unsetenv(k)
		}
	}
}

// horsTerminal : aucune variable de couleur ni de terminal forcé.
func horsTerminal(t *testing.T) {
	t.Helper()
	fixeEnv(t, map[string]string{
		"NO_COLOR": "", "CLICOLOR": "", "CLICOLOR_FORCE": "", "TTY_FORCE": "",
		"TERM": "", "COLORTERM": "",
	})
}

// ── Chaque composant hors terminal : aucun code ESC ────────────────────

// appelsUI : chaque composant de la charte, avec des arguments couvrant
// tous les statuts, couleurs de cellule et formes (détails, vide, etc.).
func appelsUI() map[string]func() {
	jeux := newUIGames([]string{"Alpha", "Béta"})
	cellule := func(row, col int, v string) lipgloss.Style {
		switch v {
		case "ok":
			return cellOK
		case "attention":
			return cellWarn
		case "erreur":
			return cellErr
		case "rien":
			return cellMuted
		}
		return cellPlain
	}
	return map[string]func(){
		"uiTitle":           func() { uiTitle("Lanceur", "sous-titre") },
		"uiTitle-nu":        func() { uiTitle("Lanceur") },
		"uiStage":           func() { uiStage(1, 3, "Dossier") },
		"uiSection":         func() { uiSection("Jeux") },
		"uiKVLine":          func() { uiKVLine("Clé", "valeur") },
		"uiKV":              func() { uiKV(0, kv{"Clé", "v"}, kv{"Réglages machine", "w"}) },
		"uiStatusLine-ok":   func() { uiStatusLine(stOK, "fait") },
		"uiStatusLine-fail": func() { uiStatusLine(stFail, "échec") },
		"uiStatusLine-skip": func() { uiStatusLine(stSkip, "ignoré") },
		"uiStatusLine-info": func() { uiStatusLine(stInfo, "en cours...") },
		"uiItem-ok":         func() { uiItem(stOK, "présent", "détail") },
		"uiItem-fail":       func() { uiItem(stFail, "absent", "d1", "d2") },
		"uiItem-skip":       func() { uiItem(stSkip, "optionnel") },
		"uiItem-info":       func() { uiItem(stInfo, "étape") },
		"uiGames.line":      func() { jeux.line(stOK, "Alpha", "fait") },
		"uiList":            func() { uiList("Jeux", []string{"Alpha", "Béta"}) },
		"uiTable": func() {
			uiTable([]string{"Jeu", "Taille"},
				[][]string{{"Alpha", "ok"}, {"Béta", "attention"}, {"Gamma", "erreur"}, {"Delta", "rien"}},
				[]int{1}, cellule)
		},
		"uiNote":         func() { uiNote("une explication", "une seconde") },
		"uiCode":         func() { uiCode("wsquashfs-launcher --doctor") },
		"uiRule":         func() { uiRule("étiquette") },
		"uiRule-nu":      func() { uiRule("") },
		"uiVerdict-vrai": func() { uiVerdict(true, "Prêt à lancer des jeux") },
		"uiVerdict-faux": func() { uiVerdict(false, "Aucun jeu ne peut être lancé") },
		"uiHelp":         func() { uiHelp(usageText) },
		"warn":           func() { warn("Message", "  → détail") },
		"uiError":        func() { uiError("Message", "  → détail") },
		"uiPrompt":       func() { uiPrompt("Supprimer les sauvegardes ?", "[o/N]") },
		"uiAuto":         func() { uiAuto("Continuer ?", "[o/N]", "n", "pas de terminal") },
	}
}

func TestHorsTerminalAucunESC(t *testing.T) {
	horsTerminal(t)
	for nom, appel := range appelsUI() {
		out, errs := capteUI(t)
		appel()
		for flux, b := range map[string]*bytes.Buffer{"stdout": out, "stderr": errs} {
			if strings.Contains(b.String(), esc) {
				t.Errorf("%s (%s) : code ESC hors terminal : %q", nom, flux, b.String())
			}
		}
		if out.Len()+errs.Len() == 0 {
			t.Errorf("%s : aucune sortie", nom)
		}
	}
}

// ── Couleurs en forçant le terminal ────────────────────────────────────

// TestCouleursForcees : CLICOLOR_FORCE=1 produit des codes ESC pour chaque
// composant ; lipgloss.Fprintln relit l'environnement à chaque appel (le
// changement de variable entre deux appels est pris en compte).
func TestCouleursForcees(t *testing.T) {
	horsTerminal(t)
	t.Setenv("CLICOLOR_FORCE", "1")
	for nom, appel := range appelsUI() {
		out, errs := capteUI(t)
		appel()
		if !strings.Contains(out.String()+errs.String(), esc) {
			t.Errorf("%s : aucun code de couleur avec CLICOLOR_FORCE=1", nom)
		}
	}
}

// TestEnvRelueÀChaqueAppel : même processus, deux appels successifs ; la
// variable retirée entre les deux change le résultat du second.
func TestEnvRelueAChaqueAppel(t *testing.T) {
	horsTerminal(t)
	out, _ := capteUI(t)
	t.Setenv("CLICOLOR_FORCE", "1")
	uiOut(sBold.Render("forcé"))
	if !strings.Contains(out.String(), esc) {
		t.Fatalf("CLICOLOR_FORCE=1 : aucun code ESC")
	}
	out.Reset()
	_ = os.Unsetenv("CLICOLOR_FORCE")
	uiOut(sBold.Render("retiré"))
	if strings.Contains(out.String(), esc) {
		t.Errorf("CLICOLOR_FORCE retiré : code ESC encore présent : %q", out.String())
	}
}

// TestNO_COLORPrioritaire : NO_COLOR=1 coupe les couleurs même avec
// CLICOLOR_FORCE=1, hors terminal, comme la règle 1 de ui.go l'annonce.
// DÉFAUT : colorprofile v0.4.3 (Detect/colorProfile) n'applique NO_COLOR
// que si la sortie est un terminal ; hors terminal CLICOLOR_FORCE l'emporte.
func TestNO_COLORPrioritaire(t *testing.T) {
	horsTerminal(t)
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("NO_COLOR", "1")
	for nom, appel := range appelsUI() {
		out, errs := capteUI(t)
		appel()
		if strings.Contains(out.String()+errs.String(), esc) {
			// DÉFAUT : voir la note ci-dessus.
			t.Errorf("DÉFAUT %s : NO_COLOR=1 ignoré avec CLICOLOR_FORCE=1 hors terminal", nom)
		}
	}
}

// TestNO_COLORSeul : NO_COLOR=1 seul, hors terminal : aucun code.
func TestNO_COLORSeul(t *testing.T) {
	horsTerminal(t)
	t.Setenv("NO_COLOR", "1")
	for nom, appel := range appelsUI() {
		out, errs := capteUI(t)
		appel()
		if strings.Contains(out.String()+errs.String(), esc) {
			t.Errorf("%s : code ESC avec NO_COLOR=1", nom)
		}
	}
}

// ── Texte exact hors terminal ──────────────────────────────────────────

// verifie compare la sortie stdout d'un appel à une chaîne écrite à la main.
func verifie(t *testing.T, nom, attendu string, appel func()) {
	t.Helper()
	out, _ := capteUI(t)
	appel()
	if got := out.String(); got != attendu {
		t.Errorf("%s :\n--- obtenu\n%s\n--- attendu\n%s", nom, got, attendu)
	}
}

func TestTexteKVLine(t *testing.T) {
	horsTerminal(t)
	// « Clé » (3 colonnes) complétée à 16 colonnes, puis « : ».
	verifie(t, "uiKVLine Clé", "Clé              : valeur\n", func() { uiKVLine("Clé", "valeur") })
	// « Nom » (3 colonnes) : même alignement.
	verifie(t, "uiKVLine Nom", "Nom              : 42\n", func() { uiKVLine("Nom", "42") })
}

func TestTexteKV(t *testing.T) {
	horsTerminal(t)
	// Clés accentuées : la colonne se cale sur « Réglages machine » (16 colonnes).
	verifie(t, "uiKV accentué", ""+
		"Réglages machine : oui\n"+
		"Clé              : non\n",
		func() { uiKV(0, kv{"Réglages machine", "oui"}, kv{"Clé", "non"}) })
	// Retrait de 2 : préfixe appliqué à chaque ligne.
	verifie(t, "uiKV retrait 2", ""+
		"  Réglages machine : oui\n"+
		"  Clé              : non\n",
		func() { uiKV(2, kv{"Réglages machine", "oui"}, kv{"Clé", "non"}) })
}

func TestTexteStatus(t *testing.T) {
	horsTerminal(t)
	verifie(t, "uiStatusLine", ""+
		"✓ fait\n"+
		"✗ échec\n"+
		"– ignoré\n"+
		"→ en cours...\n",
		func() {
			uiStatusLine(stOK, "fait")
			uiStatusLine(stFail, "échec")
			uiStatusLine(stSkip, "ignoré")
			uiStatusLine(stInfo, "en cours...")
		})
}

func TestTexteItem(t *testing.T) {
	horsTerminal(t)
	// Retrait 2 pour l'élément, retrait 6 pour chaque détail.
	verifie(t, "uiItem", ""+
		"  ✓ présent\n"+
		"      détail un\n"+
		"      détail deux\n"+
		"  ✗ absent\n"+
		"  – optionnel\n"+
		"  → étape\n",
		func() {
			uiItem(stOK, "présent", "détail un", "détail deux")
			uiItem(stFail, "absent")
			uiItem(stSkip, "optionnel")
			uiItem(stInfo, "étape")
		})
}

func TestTexteGames(t *testing.T) {
	horsTerminal(t)
	// Noms alignés sur le plus large (« Béta » et « Alpha » : 5 colonnes).
	g := newUIGames([]string{"Alpha", "Béta"})
	verifie(t, "uiGames.line", ""+
		"  ✓ Alpha  fait\n"+
		"  ✗ Béta   échec\n",
		func() {
			g.line(stOK, "Alpha", "fait")
			g.line(stFail, "Béta", "échec")
		})
	// Chaque ligne commence par exactement deux espaces puis le symbole.
	out, _ := capteUI(t)
	g.line(stSkip, "Béta", "ignoré")
	if !strings.HasPrefix(out.String(), "  – ") {
		t.Errorf("uiGames.line : retrait 2 et symbole attendus, obtenu %q", out.String())
	}
	// Colonne du message identique pour des noms de largeurs différentes
	// (position en caractères : les accents pèsent 2 octets).
	out.Reset()
	g.line(stOK, "Alpha", "M")
	g.line(stOK, "Béta", "M")
	lignes := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	pos := func(l string) int { return utf8.RuneCountInString(l[:strings.Index(l, "M")]) }
	if len(lignes) != 2 || pos(lignes[0]) != pos(lignes[1]) {
		t.Errorf("uiGames.line : messages désalignés : %q", lignes)
	}
}

func TestTexteList(t *testing.T) {
	horsTerminal(t)
	verifie(t, "uiList", ""+
		"\n"+
		"Jeux (2)\n"+
		"  · Alpha\n"+
		"  · Béta\n",
		func() { uiList("Jeux", []string{"Alpha", "Béta"}) })
}

func TestListeVide(t *testing.T) {
	horsTerminal(t)
	verifie(t, "uiList vide", "", func() { uiList("Jeux", nil) })
	verifie(t, "uiList vide (tranche vide)", "", func() { uiList("Jeux", []string{}) })
}

func TestTexteVerdict(t *testing.T) {
	horsTerminal(t)
	verifie(t, "uiVerdict vrai", "\n✓ Prêt à lancer des jeux\n",
		func() { uiVerdict(true, "Prêt à lancer des jeux") })
	verifie(t, "uiVerdict faux", "\n✗ Aucun jeu ne peut être lancé\n",
		func() { uiVerdict(false, "Aucun jeu ne peut être lancé") })
}

func TestTexteTitre(t *testing.T) {
	horsTerminal(t)
	// Sous-titre (10 colonnes) : cadre intérieur de 12 colonnes (1 de marge de chaque côté).
	verifie(t, "uiTitle", ""+
		"╭────────────╮\n"+
		"│ Lanceur    │\n"+
		"│ Sous-titre │\n"+
		"╰────────────╯\n",
		func() { uiTitle("Lanceur", "Sous-titre") })
	// Sans sous-titre : le cadre s'adapte au titre (7 colonnes + 2 de marge).
	verifie(t, "uiTitle sans sous-titre", ""+
		"╭─────────╮\n"+
		"│ Lanceur │\n"+
		"╰─────────╯\n",
		func() { uiTitle("Lanceur", "") })
}

func TestTexteStageSectionRule(t *testing.T) {
	horsTerminal(t)
	verifie(t, "uiStage", "\n▍2/5  Jeux\n", func() { uiStage(2, 5, "Jeux") })
	verifie(t, "uiSection", "\n▍Montage\n", func() { uiSection("Montage") })
	verifie(t, "uiRule", strings.Repeat("─", 41)+" étiquette\n", func() { uiRule("étiquette") })
	verifie(t, "uiRule vide", strings.Repeat("─", 41)+"\n", func() { uiRule("") })
}

func TestTexteNoteCodeAuto(t *testing.T) {
	horsTerminal(t)
	verifie(t, "uiNote", "  une explication\n  une seconde\n",
		func() { uiNote("une explication", "une seconde") })
	verifie(t, "uiCode", "  wsquashfs-launcher --doctor\n",
		func() { uiCode("wsquashfs-launcher --doctor") })
	verifie(t, "uiAuto", "  ? Continuer ? [o/N] n (pas de terminal)\n",
		func() { uiAuto("Continuer ?", "[o/N]", "n", "pas de terminal") })
}

func TestTexteErreurs(t *testing.T) {
	horsTerminal(t)
	out, errs := capteUI(t)
	warn("Message", "  → détail")
	uiError("Erreur lue", "  → cause")
	if out.Len() != 0 {
		t.Errorf("warn/uiError écrivent sur stdout : %q", out.String())
	}
	if want := "Attention : Message\n  → détail\nErreur : Erreur lue\n  → cause\n"; errs.String() != want {
		t.Errorf("stderr :\n--- obtenu\n%s--- attendu\n%s", errs.String(), want)
	}
}

func TestTextePrompt(t *testing.T) {
	horsTerminal(t)
	// Pas de fin de ligne : la réponse est saisie à la suite.
	verifie(t, "uiPrompt", "  ? Supprimer les sauvegardes ? [o/N] ",
		func() { uiPrompt("Supprimer les sauvegardes ?", "[o/N]") })
}

// ── Tableau ────────────────────────────────────────────────────────────

func TestTexteTable(t *testing.T) {
	horsTerminal(t)
	verifie(t, "uiTable", ""+
		"╭───────┬────────╮\n"+
		"│ Jeu   │ Taille │\n"+
		"├───────┼────────┤\n"+
		"│ Alpha │  12 Mo │\n"+
		"│ Béta  │ 1,5 Go │\n"+
		"╰───────┴────────╯\n",
		func() {
			uiTable([]string{"Jeu", "Taille"},
				[][]string{{"Alpha", "12 Mo"}, {"Béta", "1,5 Go"}},
				[]int{1}, nil)
		})
}

// ── Aucun espace en fin de ligne (trimLines) ───────────────────────────

func verifieSansEspaceFin(t *testing.T, nom string, appel func()) {
	t.Helper()
	out, _ := capteUI(t)
	appel()
	for i, l := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if strings.HasSuffix(l, " ") {
			t.Errorf("%s : ligne %d avec espace en fin : %q", nom, i+1, l)
		}
	}
}

func TestSansEspaceFinDeLigne(t *testing.T) {
	horsTerminal(t)
	verifieSansEspaceFin(t, "uiTable", func() {
		uiTable([]string{"Jeu", "Taille"},
			[][]string{{"Alpha", "12 Mo"}, {"Béta", "1,5 Go"}}, []int{1}, nil)
	})
	verifieSansEspaceFin(t, "uiTitle", func() { uiTitle("Lanceur", "Sous-titre") })
	// Valeur vide : « Clé : » doit sortir sans espace final.
	verifie(t, "uiKV valeur vide", "Clé :\n", func() { uiKV(0, kv{"Clé", ""}) })
	verifieSansEspaceFin(t, "uiKV", func() { uiKV(0, kv{"Clé", ""}, kv{"Réglages machine", "x"}) })
}

// ── Utilitaires ────────────────────────────────────────────────────────

func TestTruncate(t *testing.T) {
	cas := []struct {
		entree string
		max    int
		want   string
	}{
		{"Alpha", 10, "Alpha"},
		{"Alpha", 5, "Alpha"},
		{"Alphabet", 5, "Alph…"},
		{"Élodie Électrique", 6, "Élodi…"},
		{"🎮🎮🎮🎮🎮", 3, "🎮🎮…"},
		{"abc", 1, "…"},
	}
	for _, c := range cas {
		got := truncate(c.entree, c.max)
		if got != c.want {
			t.Errorf("truncate(%q, %d) = %q, attendu %q", c.entree, c.max, got, c.want)
		}
	}
}

// TestTruncateUTF8 : aucune coupure au milieu d'un caractère, longueur
// (en caractères) respectée, pour toutes les longueurs cibles.
func TestTruncateUTF8(t *testing.T) {
	const s = "Élodie 🎮 Électrique – Ça"
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := truncate(s, max)
		if !utf8.ValidString(got) {
			t.Errorf("truncate(%d) : UTF-8 invalide : %q", max, got)
		}
		if n := utf8.RuneCountInString(got); n > max {
			t.Errorf("truncate(%d) : %d caractères, maximum dépassé", max, n)
		}
	}
}

func TestPadRightEtLargeur(t *testing.T) {
	if got := padRight("Clé", 6); got != "Clé   " {
		t.Errorf("padRight(Clé, 6) = %q", got)
	}
	if got := padRight("Éclair", 3); got != "Éclair" {
		t.Errorf("padRight ne doit jamais tronquer : %q", got)
	}
	if got := textWidth("Clé"); got != 3 {
		t.Errorf("textWidth(Clé) = %d, attendu 3", got)
	}
	// Les codes de style ne comptent pas dans la largeur.
	if got := textWidth(sBold.Render("Gras")); got != 4 {
		t.Errorf("textWidth(gras) = %d, attendu 4", got)
	}
}

// TestCléLaPlusLongueTientDansLaColonne : « Réglages machine » (16 colonnes)
// tient dans uiKeyWidth, comme le commentaire de ui.go l'affirme : sinon
// uiKVLine ne calerait plus « : » sur la colonne des autres clés.
func TestCléLaPlusLongueTientDansLaColonne(t *testing.T) {
	if n := textWidth("Réglages machine"); n > uiKeyWidth {
		t.Errorf("« Réglages machine » fait %d colonnes, uiKeyWidth = %d", n, uiKeyWidth)
	}
}

// TestUiKVLineAligne : les clés courtes sont alignées sur uiKeyWidth.
func TestUiKVLineAligne(t *testing.T) {
	horsTerminal(t)
	out, _ := capteUI(t)
	uiKVLine("Clé", "a")
	uiKVLine("Réglages", "b")
	lignes := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lignes) != 2 {
		t.Fatalf("deux lignes attendues, obtenu %q", lignes)
	}
	// Contrat écrit en dur (16 colonnes) : la constante seule ne suffit pas.
	const colonne = 16
	for _, l := range lignes {
		if i := strings.Index(l, " :"); utf8.RuneCountInString(l[:i]) != colonne {
			t.Errorf("« : » hors colonne %d : %q", colonne, l)
		}
	}
}

func TestSizeText(t *testing.T) {
	cas := map[int64]string{
		0:                      "0 o",
		1023:                   "1023 o",
		1024:                   "1 Ko",
		1024*1024 - 1:          "1023 Ko",
		1024 * 1024:            "1 Mo",
		5 * 1024 * 1024:        "5 Mo",
		1024 * 1024 * 1024:     "1,0 Go",
		1610612736:             "1,5 Go", // 1,5 Gio
		2 * 1024 * 1024 * 1024: "2,0 Go",
	}
	for n, want := range cas {
		if got := sizeText(n); got != want {
			t.Errorf("sizeText(%d) = %q, attendu %q", n, got, want)
		}
	}
}

func TestPlural(t *testing.T) {
	cas := []struct {
		n    int
		want string
	}{
		{0, "0 jeu"},
		{1, "1 jeu"},
		{2, "2 jeux"},
		{3, "3 jeux"},
	}
	for _, c := range cas {
		if got := plural(c.n, "jeu", "jeux"); got != c.want {
			t.Errorf("plural(%d) = %q, attendu %q", c.n, got, c.want)
		}
	}
}

// ── Aide ───────────────────────────────────────────────────────────────

// TestHelpIdentique : hors terminal, uiHelp rend usage.txt octet pour octet.
func TestHelpIdentique(t *testing.T) {
	horsTerminal(t)
	verifie(t, "uiHelp", usageText, func() { uiHelp(usageText) })
}

// TestHelpSansEspaceFin : usage.txt ne doit contenir aucun espace de fin de
// ligne (sinon uiOut les retire et la sortie diverge du fichier).
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

// TestDoctorJSONSansCouleur : --doctor --json reste du JSON pur, sans ESC,
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
