package ui

// Tests de la charte d'affichage (ui.go) : jamais de couleur hors terminal.
// Out et Err écrivent dans stdout / stderr, remplacés ici par des
// bytes.Buffer (« hors terminal »).

import (
	"bytes"
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
	oldOut, oldErr := stdout, stderr
	stdout, stderr = out, errs
	t.Cleanup(func() { stdout, stderr = oldOut, oldErr })
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
	jeux := NewGames([]string{"Alpha", "Béta"})
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
		"Title":           func() { Title("Lanceur", "sous-titre") },
		"Title-nu":        func() { Title("Lanceur") },
		"Stage":           func() { Stage(1, 3, "Dossier") },
		"Section":         func() { Section("Jeux") },
		"KVLine":          func() { KVLine("Clé", "valeur") },
		"KV":              func() { KV(0, Pair{"Clé", "v"}, Pair{"Réglages machine", "w"}) },
		"StatusLine-ok":   func() { StatusLine(OK, "fait") },
		"StatusLine-fail": func() { StatusLine(Fail, "échec") },
		"StatusLine-skip": func() { StatusLine(Skip, "ignoré") },
		"StatusLine-info": func() { StatusLine(Info, "en cours...") },
		"Item-ok":         func() { Item(OK, "présent", "détail") },
		"Item-fail":       func() { Item(Fail, "absent", "d1", "d2") },
		"Item-skip":       func() { Item(Skip, "optionnel") },
		"Item-info":       func() { Item(Info, "étape") },
		"Games.line":      func() { jeux.Line(OK, "Alpha", "fait") },
		"List":            func() { List("Jeux", []string{"Alpha", "Béta"}) },
		"Table": func() {
			Table([]string{"Jeu", "Taille"},
				[][]string{{"Alpha", "ok"}, {"Béta", "attention"}, {"Gamma", "erreur"}, {"Delta", "rien"}},
				[]int{1}, cellule)
		},
		"Note":         func() { Note("une explication", "une seconde") },
		"Code":         func() { Code("wsquashfs-launcher --doctor") },
		"Rule":         func() { Rule("étiquette") },
		"Rule-nu":      func() { Rule("") },
		"Verdict-vrai": func() { Verdict(true, "Prêt à lancer des jeux") },
		"Verdict-faux": func() { Verdict(false, "Aucun jeu ne peut être lancé") },
		"Help":         func() { Help(aideTest) },
		"Warn":         func() { Warn("Message", "  → détail") },
		"Error":        func() { Error("Message", "  → détail") },
		"Prompt":       func() { Prompt("Supprimer les sauvegardes ?", "[o/N]") },
		"Auto":         func() { Auto("Continuer ?", "[o/N]", "n", "pas de terminal") },
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
	Out(sBold.Render("forcé"))
	if !strings.Contains(out.String(), esc) {
		t.Fatalf("CLICOLOR_FORCE=1 : aucun code ESC")
	}
	out.Reset()
	_ = os.Unsetenv("CLICOLOR_FORCE")
	Out(sBold.Render("retiré"))
	if strings.Contains(out.String(), esc) {
		t.Errorf("CLICOLOR_FORCE retiré : code ESC encore présent : %q", out.String())
	}
}

// TestNO_COLORPrioritaire : NO_COLOR=1 coupe les couleurs même avec
// CLICOLOR_FORCE=1, hors terminal, comme la règle 1 de ui.go l'annonce.
// colorprofile v0.4.3 (Detect/colorProfile) n'applique NO_COLOR que si la
// sortie est un terminal : write retire lui-même les codes (correctif
// détecté par ce test).
func TestNO_COLORPrioritaire(t *testing.T) {
	horsTerminal(t)
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("NO_COLOR", "1")
	for nom, appel := range appelsUI() {
		out, errs := capteUI(t)
		appel()
		if strings.Contains(out.String()+errs.String(), esc) {
			t.Errorf("%s : NO_COLOR=1 ignoré avec CLICOLOR_FORCE=1 hors terminal", nom)
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
	verifie(t, "KVLine Clé", "Clé              : valeur\n", func() { KVLine("Clé", "valeur") })
	// « Nom » (3 colonnes) : même alignement.
	verifie(t, "KVLine Nom", "Nom              : 42\n", func() { KVLine("Nom", "42") })
}

func TestTexteKV(t *testing.T) {
	horsTerminal(t)
	// Clés accentuées : la colonne se cale sur « Réglages machine » (16 colonnes).
	verifie(t, "KV accentué", ""+
		"Réglages machine : oui\n"+
		"Clé              : non\n",
		func() { KV(0, Pair{"Réglages machine", "oui"}, Pair{"Clé", "non"}) })
	// Retrait de 2 : préfixe appliqué à chaque ligne.
	verifie(t, "KV retrait 2", ""+
		"  Réglages machine : oui\n"+
		"  Clé              : non\n",
		func() { KV(2, Pair{"Réglages machine", "oui"}, Pair{"Clé", "non"}) })
}

func TestTexteStatus(t *testing.T) {
	horsTerminal(t)
	verifie(t, "StatusLine", ""+
		"✓ fait\n"+
		"✗ échec\n"+
		"– ignoré\n"+
		"→ en cours...\n",
		func() {
			StatusLine(OK, "fait")
			StatusLine(Fail, "échec")
			StatusLine(Skip, "ignoré")
			StatusLine(Info, "en cours...")
		})
}

func TestTexteItem(t *testing.T) {
	horsTerminal(t)
	// Retrait 2 pour l'élément, retrait 6 pour chaque détail.
	verifie(t, "Item", ""+
		"  ✓ présent\n"+
		"      détail un\n"+
		"      détail deux\n"+
		"  ✗ absent\n"+
		"  – optionnel\n"+
		"  → étape\n",
		func() {
			Item(OK, "présent", "détail un", "détail deux")
			Item(Fail, "absent")
			Item(Skip, "optionnel")
			Item(Info, "étape")
		})
}

func TestTexteGames(t *testing.T) {
	horsTerminal(t)
	// Noms alignés sur le plus large (« Béta » et « Alpha » : 5 colonnes).
	g := NewGames([]string{"Alpha", "Béta"})
	verifie(t, "Games.line", ""+
		"  ✓ Alpha  fait\n"+
		"  ✗ Béta   échec\n",
		func() {
			g.Line(OK, "Alpha", "fait")
			g.Line(Fail, "Béta", "échec")
		})
	// Chaque ligne commence par exactement deux espaces puis le symbole.
	out, _ := capteUI(t)
	g.Line(Skip, "Béta", "ignoré")
	if !strings.HasPrefix(out.String(), "  – ") {
		t.Errorf("Games.line : retrait 2 et symbole attendus, obtenu %q", out.String())
	}
	// Colonne du message identique pour des noms de largeurs différentes
	// (position en caractères : les accents pèsent 2 octets).
	out.Reset()
	g.Line(OK, "Alpha", "M")
	g.Line(OK, "Béta", "M")
	lignes := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	pos := func(l string) int { return utf8.RuneCountInString(l[:strings.Index(l, "M")]) }
	if len(lignes) != 2 || pos(lignes[0]) != pos(lignes[1]) {
		t.Errorf("Games.line : messages désalignés : %q", lignes)
	}
}

func TestTexteList(t *testing.T) {
	horsTerminal(t)
	verifie(t, "List", ""+
		"\n"+
		"Jeux (2)\n"+
		"  · Alpha\n"+
		"  · Béta\n",
		func() { List("Jeux", []string{"Alpha", "Béta"}) })
}

func TestListeVide(t *testing.T) {
	horsTerminal(t)
	verifie(t, "List vide", "", func() { List("Jeux", nil) })
	verifie(t, "List vide (tranche vide)", "", func() { List("Jeux", []string{}) })
}

func TestTexteVerdict(t *testing.T) {
	horsTerminal(t)
	verifie(t, "Verdict vrai", "\n✓ Prêt à lancer des jeux\n",
		func() { Verdict(true, "Prêt à lancer des jeux") })
	verifie(t, "Verdict faux", "\n✗ Aucun jeu ne peut être lancé\n",
		func() { Verdict(false, "Aucun jeu ne peut être lancé") })
}

func TestTexteTitre(t *testing.T) {
	horsTerminal(t)
	// Sous-titre (10 colonnes) : cadre intérieur de 12 colonnes (1 de marge de chaque côté).
	verifie(t, "Title", ""+
		"╭────────────╮\n"+
		"│ Lanceur    │\n"+
		"│ Sous-titre │\n"+
		"╰────────────╯\n",
		func() { Title("Lanceur", "Sous-titre") })
	// Sans sous-titre : le cadre s'adapte au titre (7 colonnes + 2 de marge).
	verifie(t, "Title sans sous-titre", ""+
		"╭─────────╮\n"+
		"│ Lanceur │\n"+
		"╰─────────╯\n",
		func() { Title("Lanceur", "") })
}

func TestTexteStageSectionRule(t *testing.T) {
	horsTerminal(t)
	verifie(t, "Stage", "\n▍2/5  Jeux\n", func() { Stage(2, 5, "Jeux") })
	verifie(t, "Section", "\n▍Montage\n", func() { Section("Montage") })
	verifie(t, "Rule", strings.Repeat("─", 41)+" étiquette\n", func() { Rule("étiquette") })
	verifie(t, "Rule vide", strings.Repeat("─", 41)+"\n", func() { Rule("") })
}

func TestTexteNoteCodeAuto(t *testing.T) {
	horsTerminal(t)
	verifie(t, "Note", "  une explication\n  une seconde\n",
		func() { Note("une explication", "une seconde") })
	verifie(t, "Code", "  wsquashfs-launcher --doctor\n",
		func() { Code("wsquashfs-launcher --doctor") })
	verifie(t, "Auto", "  ? Continuer ? [o/N] n (pas de terminal)\n",
		func() { Auto("Continuer ?", "[o/N]", "n", "pas de terminal") })
}

func TestTexteErreurs(t *testing.T) {
	horsTerminal(t)
	out, errs := capteUI(t)
	Warn("Message", "  → détail")
	Error("Erreur lue", "  → cause")
	if out.Len() != 0 {
		t.Errorf("Warn/Error écrivent sur stdout : %q", out.String())
	}
	if want := "Attention : Message\n  → détail\nErreur : Erreur lue\n  → cause\n"; errs.String() != want {
		t.Errorf("stderr :\n--- obtenu\n%s--- attendu\n%s", errs.String(), want)
	}
}

func TestTextePrompt(t *testing.T) {
	horsTerminal(t)
	// Pas de fin de ligne : la réponse est saisie à la suite.
	verifie(t, "Prompt", "  ? Supprimer les sauvegardes ? [o/N] ",
		func() { Prompt("Supprimer les sauvegardes ?", "[o/N]") })
}

// ── Tableau ────────────────────────────────────────────────────────────

func TestTexteTable(t *testing.T) {
	horsTerminal(t)
	verifie(t, "Table", ""+
		"╭───────┬────────╮\n"+
		"│ Jeu   │ Taille │\n"+
		"├───────┼────────┤\n"+
		"│ Alpha │  12 Mo │\n"+
		"│ Béta  │ 1,5 Go │\n"+
		"╰───────┴────────╯\n",
		func() {
			Table([]string{"Jeu", "Taille"},
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
	verifieSansEspaceFin(t, "Table", func() {
		Table([]string{"Jeu", "Taille"},
			[][]string{{"Alpha", "12 Mo"}, {"Béta", "1,5 Go"}}, []int{1}, nil)
	})
	verifieSansEspaceFin(t, "Title", func() { Title("Lanceur", "Sous-titre") })
	// Valeur vide : « Clé : » doit sortir sans espace final.
	verifie(t, "KV valeur vide", "Clé :\n", func() { KV(0, Pair{"Clé", ""}) })
	verifieSansEspaceFin(t, "KV", func() { KV(0, Pair{"Clé", ""}, Pair{"Réglages machine", "x"}) })
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
		got := Truncate(c.entree, c.max)
		if got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, attendu %q", c.entree, c.max, got, c.want)
		}
	}
}

// TestTruncateUTF8 : aucune coupure au milieu d'un caractère, longueur
// (en caractères) respectée, pour toutes les longueurs cibles.
func TestTruncateUTF8(t *testing.T) {
	const s = "Élodie 🎮 Électrique – Ça"
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := Truncate(s, max)
		if !utf8.ValidString(got) {
			t.Errorf("Truncate(%d) : UTF-8 invalide : %q", max, got)
		}
		if n := utf8.RuneCountInString(got); n > max {
			t.Errorf("Truncate(%d) : %d caractères, maximum dépassé", max, n)
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
// tient dans keyWidth, comme le commentaire de ui.go l'affirme : sinon
// KVLine ne calerait plus « : » sur la colonne des autres clés.
func TestCléLaPlusLongueTientDansLaColonne(t *testing.T) {
	if n := textWidth("Réglages machine"); n > keyWidth {
		t.Errorf("« Réglages machine » fait %d colonnes, keyWidth = %d", n, keyWidth)
	}
}

// TestUiKVLineAligne : les clés courtes sont alignées sur keyWidth.
func TestUiKVLineAligne(t *testing.T) {
	horsTerminal(t)
	out, _ := capteUI(t)
	KVLine("Clé", "a")
	KVLine("Réglages", "b")
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
		if got := SizeText(n); got != want {
			t.Errorf("SizeText(%d) = %q, attendu %q", n, got, want)
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
		if got := Plural(c.n, "jeu", "jeux"); got != c.want {
			t.Errorf("Plural(%d) = %q, attendu %q", c.n, got, c.want)
		}
	}
}

// ── Aide ───────────────────────────────────────────────────────────────

const aideTest = "Usage :\n  wsquashfs-launcher --help\n\nOptions :\n  --doctor  diagnostic\n"

// TestHelpIdentique : hors terminal, Help rend le texte octet pour octet.
func TestHelpIdentique(t *testing.T) {
	horsTerminal(t)
	verifie(t, "Help", aideTest, func() { Help(aideTest) })
}

// TestFitColumns : un tableau plus large que le terminal est rogné, sans
// toucher aux colonnes alignées à droite ni aux en-têtes ; hors terminal
// (largeur 0), rien ne change.
func TestFitColumns(t *testing.T) {
	headers := []string{"Jeu", "Taille", "Image"}
	rows := [][]string{
		{"Teenage Mutant Ninja Turtles Arcade Wrath Of The Mutants", "1,1 Go", "introuvable : /userdata/roms/windows/Teenage Mutant Ninja Turtles Arcade.wsquashfs"},
		{"Old32", "16 Ko", "/roms/Old32.wsquashfs"},
	}
	if got := fitColumns(headers, rows, map[int]bool{1: true}, 0); got[0][0] != rows[0][0] {
		t.Fatal("rogné sans terminal")
	}
	got := fitColumns(headers, rows, map[int]bool{1: true}, 80)
	total := 1
	for c := range headers {
		w := textWidth(headers[c])
		for _, r := range got {
			w = max(w, textWidth(r[c]))
		}
		total += w + 3
	}
	if total > 80 {
		t.Errorf("tableau de %d colonnes pour un terminal de 80 : %q", total, got)
	}
	if got[0][1] != "1,1 Go" || got[1][0] != "Old32" {
		t.Errorf("colonne alignée à droite ou cellule courte modifiée : %q", got)
	}
	if !strings.HasPrefix(got[0][2], "introuvable") || !strings.HasSuffix(got[0][2], ".wsquashfs") || !strings.Contains(got[0][2], "…") {
		t.Errorf("chemin mal abrégé (début et fin à garder) : %q", got[0][2])
	}
	if !strings.HasSuffix(got[0][0], "…") || !utf8.ValidString(got[0][0]) {
		t.Errorf("nom mal abrégé : %q", got[0][0])
	}
}

// TestQuestionsHomogenes : toujours « [o/n] » dans le même ordre, la réponse
// par défaut écrite en toutes lettres.
func TestQuestionsHomogenes(t *testing.T) {
	if YesNo(true) != "[o/n] (défaut : oui)" || YesNo(false) != "[o/n] (défaut : non)" {
		t.Errorf("indications : %q / %q", YesNo(true), YesNo(false))
	}
	out, _ := capteUI(t)
	Menu([]string{"Tout", "Rien"}, "2")
	want := "  1  Tout\n  2  Rien  (défaut)\n"
	if out.String() != want {
		t.Errorf("menu :\n%q\nattendu :\n%q", out.String(), want)
	}
}

func TestTextWidthAndPad(t *testing.T) {
	if w := textWidth("Développé"); w != 9 {
		t.Errorf("textWidth = %d, attendu 9", w)
	}
	if p := padRight("Développé", 12); p != "Développé   " {
		t.Errorf("pad = %q", p)
	}
}
