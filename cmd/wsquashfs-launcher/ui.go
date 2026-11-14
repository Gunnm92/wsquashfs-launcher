package main

// ui.go : règles et fonctions d'affichage du lanceur. Tout ce qui s'affiche
// pour un humain passe par ce fichier.
//
// Règles
//  1. Le texte fait foi : symboles, mots et structure disent tout ; couleur
//     et gras ne font que les souligner. Hors terminal (tube, fichier,
//     frontend, agent, tests, NO_COLOR, TERM=dumb), aucun code de couleur :
//     le texte seul, identique.
//  2. Tout s'écrit par uiOut / uiErr (lipgloss.Fprintln : codes retirés quand
//     la sortie n'est pas un terminal). Jamais de fmt.Print d'un texte stylé,
//     jamais de style stocké dans une donnée réutilisée (JSON, comparaison).
//  3. 16 couleurs de base seulement (le thème du terminal les adapte) ; pas de
//     couleur adaptative qui interroge le terminal.
//  4. Un même type d'information a toujours le même rendu, dans toutes les
//     commandes :
//       uiTitle    titre de la commande, encadré (un par commande)
//       uiStage    étape numérotée d'un parcours (--setup)
//       uiSection  groupe d'éléments sous le titre
//       uiKV       « Clé : valeur » alignés (uiKVLine au fil de l'eau)
//       uiStatus   action globale : ✓ fait, ✗ échec, – ignoré, → en cours
//       uiItem     élément d'une section, même symboles, détails en dessous
//       uiGames    une ligne par jeu, noms en colonne
//       uiList     rubrique suivie de noms
//       uiTable    tableau encadré, en-tête en gras
//       uiNote     explication, simulation, astuce (discrète)
//       uiCode     texte à copier, seul sur sa ligne
//       uiRule     séparateur (sortie du jeu seulement)
//       uiVerdict  dernière ligne d'un rapport
//       uiAsk*     questions, une seule forme
//       warn       « Attention : » sur stderr, la commande continue
//       die        « Erreur : » sur stderr, la commande s'arrête (code 1)
//  5. Sorties machine hors charte : JSON (--doctor json, --trial, agent
//     HTTP) et journal de l'agent passent par encoding/json ou fmt directement.
//  6. Retraits : 0 pour titres, sections, statuts globaux, clés-valeurs au fil
//     de l'eau et verdicts ; 2 pour les éléments, listes, notes et lignes de
//     jeu (le manager filtre les lignes de --saves qui commencent par deux
//     espaces) ; 6 pour les détails d'un élément.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// ── Palette et styles ──────────────────────────────────────────────────

var (
	cAccent = lipgloss.Cyan
	cOK     = lipgloss.Green
	cWarn   = lipgloss.Yellow
	cErr    = lipgloss.Red
	cMuted  = lipgloss.BrightBlack
)

var (
	sTitle   = lipgloss.NewStyle().Bold(true).Foreground(cAccent).Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 1)
	sSection = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sBold    = lipgloss.NewStyle().Bold(true)
	sMuted   = lipgloss.NewStyle().Foreground(cMuted)
	sCode    = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sWarn    = lipgloss.NewStyle().Bold(true).Foreground(cWarn)
	sErr     = lipgloss.NewStyle().Bold(true).Foreground(cErr)
	sOK      = lipgloss.NewStyle().Bold(true).Foreground(cOK)
)

// ── Écriture ───────────────────────────────────────────────────────────

// uiStdout, uiStderr : destinations (remplacées par les tests).
var (
	uiStdout io.Writer = os.Stdout
	uiStderr io.Writer = os.Stderr
)

// uiOut : une ligne sur la sortie standard (codes retirés hors terminal).
// Les espaces de fin de ligne sont retirés (tableaux, encadrés).
func uiOut(s string) { uiWrite(uiStdout, trimLines(s)+"\n") }

// uiErr : une ligne sur la sortie d'erreur.
func uiErr(s string) { uiWrite(uiStderr, trimLines(s)+"\n") }

// uiWrite : écriture par lipgloss (codes retirés hors terminal). NO_COLOR
// l'emporte sur tout, CLICOLOR_FORCE compris (no-color.org) : colorprofile
// v0.4.3 garde les couleurs hors terminal quand les deux sont posés.
func uiWrite(w io.Writer, s string) {
	if os.Getenv("NO_COLOR") != "" {
		s = ansi.Strip(s)
	}
	_, _ = lipgloss.Fprint(w, s)
}

// trimLines : espaces de fin de chaque ligne retirés.
func trimLines(s string) string {
	if !strings.Contains(s, " \n") && !strings.HasSuffix(s, " ") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

// ── Statuts ────────────────────────────────────────────────────────────

type uiStatus int

const (
	stOK   uiStatus = iota // ✓ fait, présent
	stFail                 // ✗ échec d'un élément (la commande continue)
	stSkip                 // – ignoré, optionnel, non bloquant
	stInfo                 // → étape en cours, information neutre
)

// mark : symbole coloré d'un statut.
func (st uiStatus) mark() string {
	switch st {
	case stOK:
		return sOK.Render("✓")
	case stFail:
		return sErr.Render("✗")
	case stSkip:
		return sWarn.Render("–")
	}
	return sCode.Render("→")
}

// uiStatusLine : action globale (retrait 0). Un message stInfo se termine
// par « ... » s'il décrit une action en cours (à la charge de l'appelant).
func uiStatusLine(st uiStatus, msg string) { uiOut(st.mark() + " " + msg) }

// uiItem : élément d'une section (retrait 2), détails à 6 espaces.
func uiItem(st uiStatus, msg string, details ...string) {
	uiOut("  " + st.mark() + " " + msg)
	for _, d := range details {
		uiOut("      " + sMuted.Render(d))
	}
}

// ── Titres ─────────────────────────────────────────────────────────────

// uiTitle : titre de la commande, encadré ; sous-titre en dessous, discret.
func uiTitle(text string, subtitle ...string) {
	body := sBold.Render(text)
	for _, s := range subtitle {
		if s != "" {
			body += "\n" + sMuted.Render(s)
		}
	}
	uiOut(sTitle.Render(body))
}

// uiSection : groupe d'éléments, précédé d'une ligne vide.
func uiSection(text string) {
	uiOut("")
	uiOut(sSection.Render("▍" + text))
}

// uiStage : étape numérotée d'un parcours (--setup), précédée d'une ligne vide.
func uiStage(n, total int, text string) {
	uiOut("")
	uiOut(sSection.Render(fmt.Sprintf("▍%d/%d  %s", n, total, text)))
}

// ── Clés-valeurs ───────────────────────────────────────────────────────

// uiKeyWidth : colonne des clés émises au fil de l'eau (uiKVLine) ; la plus
// longue clé du lancement (« Réglages machine ») y tient.
const uiKeyWidth = 16

type kv struct{ k, v string }

// uiKVLine : une paire, clé sur uiKeyWidth colonnes (retrait 0).
func uiKVLine(key, value string) {
	uiOut(sMuted.Render(padRight(key, uiKeyWidth)+" :") + " " + value)
}

// uiKV : bloc de paires alignées sur la plus longue clé.
func uiKV(indent int, pairs ...kv) {
	w := 0
	for _, p := range pairs {
		w = max(w, textWidth(p.k))
	}
	for _, p := range pairs {
		uiOut(strings.Repeat(" ", indent) + sMuted.Render(padRight(p.k, w)+" :") + " " + p.v)
	}
}

// ── Jeux, listes, tableaux ─────────────────────────────────────────────

// uiNameMax : au-delà, un nom de jeu est abrégé (« … »).
const uiNameMax = 48

// uiGames : lignes par jeu, noms alignés sur la colonne la plus large.
type uiGames struct{ width int }

func newUIGames(names []string) uiGames {
	w := 3
	for _, n := range names {
		w = max(w, textWidth(truncate(n, uiNameMax)))
	}
	return uiGames{w}
}

// line : « ␣␣✓ Nom   message » (retrait 2, contrat du manager pour --saves).
func (g uiGames) line(st uiStatus, name, msg string) {
	uiOut("  " + st.mark() + " " + padRight(truncate(name, uiNameMax), g.width) + "  " + msg)
}

// uiList : rubrique en gras avec le nombre d'éléments, puis un nom par ligne
// (retrait 2). Rien si la liste est vide.
func uiList(heading string, names []string) {
	if len(names) == 0 {
		return
	}
	uiOut("")
	uiOut(sBold.Render(fmt.Sprintf("%s (%d)", heading, len(names))))
	for _, n := range names {
		uiOut("  " + sMuted.Render("·") + " " + n)
	}
}

// uiCell : style d'une cellule de tableau selon sa valeur (nil : aucun).
type uiCell func(row, col int, value string) lipgloss.Style

// uiTable : tableau encadré, en-tête en gras ; right : colonnes alignées à
// droite (nombres, tailles).
//
// Plus large que le terminal : les colonnes de texte les plus larges sont
// rognées (« … » ; un chemin garde son début et sa fin). Hors terminal, rien
// n'est rogné : la sortie ne dépend pas de la machine.
func uiTable(headers []string, rows [][]string, right []int, cell uiCell) {
	isRight := map[int]bool{}
	for _, c := range right {
		isRight[c] = true
	}
	rows = fitColumns(headers, rows, isRight, uiTermWidth())
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(sMuted).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle()
			switch {
			case row == table.HeaderRow:
				s = s.Bold(true).Foreground(cAccent)
			case cell != nil && row < len(rows) && col < len(rows[row]):
				s = cell(row, col, rows[row][col])
			}
			// Marge et alignement posés en dernier : Inherit ne les copie pas.
			s = s.Padding(0, 1)
			if isRight[col] {
				s = s.Align(lipgloss.Right)
			}
			return s
		})
	uiOut(t.Render())
}

// uiTermWidth : largeur du terminal de la sortie standard, 0 hors terminal.
var uiTermWidth = func() int {
	f, ok := uiStdout.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) {
		return 0
	}
	w, _, err := term.GetSize(f.Fd())
	if err != nil {
		return 0
	}
	return w
}

// uiMinColumn : largeur en dessous de laquelle une colonne n'est plus rognée.
const uiMinColumn = 12

// fitColumns : cellules rognées pour que le tableau tienne dans width
// colonnes (0 : aucune limite). Les colonnes alignées à droite (nombres,
// tailles) et les en-têtes ne sont jamais rognés ; la plus large des autres
// perd un caractère à la fois.
func fitColumns(headers []string, rows [][]string, right map[int]bool, width int) [][]string {
	if width <= 0 {
		return rows
	}
	n := len(headers)
	w := make([]int, n)
	for c, h := range headers {
		w[c] = textWidth(h)
	}
	for _, r := range rows {
		for c, v := range r {
			if c < n {
				w[c] = max(w[c], textWidth(v))
			}
		}
	}
	total := 1 // bordure gauche ; puis par colonne : marges (2) et bordure (1)
	for _, x := range w {
		total += x + 3
	}
	for over := total - width; over > 0; over-- {
		best := -1
		for c := range w {
			if right[c] || w[c] <= max(uiMinColumn, textWidth(headers[c])) {
				continue
			}
			if best < 0 || w[c] > w[best] {
				best = c
			}
		}
		if best < 0 {
			break
		}
		w[best]--
	}
	out := make([][]string, len(rows))
	for i, r := range rows {
		out[i] = make([]string, len(r))
		for c, v := range r {
			if c < n && textWidth(v) > w[c] {
				if strings.Contains(v, "/") {
					v = truncateMiddle(v, w[c])
				} else {
					v = truncate(v, w[c])
				}
			}
			out[i][c] = v
		}
	}
	return out
}

// Styles de cellule courants.
var (
	cellOK    = lipgloss.NewStyle().Foreground(cOK).Bold(true)
	cellWarn  = lipgloss.NewStyle().Foreground(cWarn)
	cellErr   = lipgloss.NewStyle().Foreground(cErr)
	cellMuted = lipgloss.NewStyle().Foreground(cMuted)
	cellPlain = lipgloss.NewStyle()
)

// uiStateCell : colonne d'état de --list (en cours, monté ou verrouillé, -)
// et colonne d'image introuvable, colorées selon la valeur.
func uiStateCell(stateCol, imageCol int) uiCell {
	return func(_, col int, v string) lipgloss.Style {
		switch {
		case col == stateCol:
			switch v {
			case "en cours":
				return cellOK
			case "monté", "verrouillé":
				return cellWarn
			case "-":
				return cellMuted
			}
		case col == imageCol && strings.HasPrefix(v, "introuvable"):
			return cellErr
		}
		return cellPlain
	}
}

// uiBoldRow : ligne de total (row, indice de données) en gras.
func uiBoldRow(row int) uiCell {
	return func(r, _ int, _ string) lipgloss.Style {
		if r == row {
			return sBold
		}
		return cellPlain
	}
}

// ── Texte ──────────────────────────────────────────────────────────────

// uiNote : explication, simulation, astuce (retrait 2, discrète). Les
// lignes sont coupées par l'appelant (sortie identique sur toutes les largeurs).
func uiNote(lines ...string) {
	for _, l := range lines {
		uiOut("  " + sMuted.Render(l))
	}
}

// uiCode : texte à copier (code d'association, commande), seul sur sa ligne.
func uiCode(text string) { uiOut("  " + sCode.Render(text)) }

// uiRule : séparateur, avec une étiquette facultative (sortie du jeu).
func uiRule(label string) {
	line := strings.Repeat("─", 41)
	if label != "" {
		line += " " + label
	}
	uiOut(sMuted.Render(line))
}

// uiVerdict : dernière ligne d'un rapport, précédée d'une ligne vide.
func uiVerdict(ok bool, msg string) {
	uiOut("")
	if ok {
		uiOut(sOK.Render("✓ " + msg))
	} else {
		uiOut(sErr.Render("✗ " + msg))
	}
}

// uiHelp : texte d'aide fixe (usage.txt) ; rubriques (ligne en colonne 0
// finissant par « : ») en gras, commandes en couleur. Hors terminal, le texte
// est rendu à l'identique.
func uiHelp(text string) {
	for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		switch t := strings.TrimSpace(l); {
		case l != "" && l[0] != ' ' && strings.HasSuffix(l, ":"):
			uiOut(sBold.Render(l))
		case strings.HasPrefix(t, "wsquashfs-launcher"):
			indent := l[:len(l)-len(strings.TrimLeft(l, " "))]
			uiOut(indent + sCode.Render(t))
		default:
			uiOut(l)
		}
	}
}

// ── Messages d'erreur (stderr) ─────────────────────────────────────────

// warn : avertissement, la commande continue.
func warn(msg string, details ...string) {
	uiErr(sWarn.Render("Attention :") + " " + msg)
	for _, d := range details {
		uiErr(d) // retrait à la charge de l'appelant (« ␣␣→ … »)
	}
}

// uiError : « Erreur : » et ses détails, sans quitter.
func uiError(msg string, details ...string) {
	uiErr(sErr.Render("Erreur :") + " " + msg)
	for _, d := range details {
		uiErr(d) // retrait à la charge de l'appelant (« ␣␣→ … »)
	}
}

// die : erreur, puis sortie (code 1, cleanup compris).
func die(msg string, details ...string) {
	uiError(msg, details...)
	exitNow(1)
}

// uiUsage : rappel de syntaxe, sans quitter.
func uiUsage(syntax string) { uiErr(sErr.Render("Usage :") + " " + syntax) }

// usageError : rappel de syntaxe, puis code 1.
func usageError(syntax string) {
	uiUsage(syntax)
	exitNow(1)
}

// ── Questions ──────────────────────────────────────────────────────────
//
// Une seule forme : « ␣␣? Question [o/n] (défaut : non) » ; toujours
// « o/n » dans cet ordre, la réponse par défaut écrite en toutes lettres
// (uiYesNo). Réponse automatique affichée
// en clair : « o (yes) », « n (pas de terminal) ».

func uiQuestion(q, hint string) string {
	return "  " + sCode.Render("?") + " " + q + " " + sMuted.Render(hint)
}

// uiYesNo : indication d'une question oui/non, identique partout.
func uiYesNo(def bool) string {
	if def {
		return "[o/n] (défaut : oui)"
	}
	return "[o/n] (défaut : non)"
}

// uiMenu : choix numérotés (« ␣␣1  libellé »), le choix par défaut marqué ;
// suivi d'une question askChoice. def : numéro par défaut (« 2 »).
func uiMenu(options []string, def string) {
	for i, o := range options {
		n := fmt.Sprint(i + 1)
		line := "  " + sCode.Render(n) + "  " + o
		if n == def {
			line += "  " + sMuted.Render("(défaut)")
		}
		uiOut(line)
	}
}

// uiPrompt : question sans fin de ligne (la réponse est saisie à la suite).
func uiPrompt(q, hint string) {
	uiWrite(uiStdout, uiQuestion(q, hint)+" ")
}

// uiAuto : question à réponse automatique, affichée avec sa raison.
func uiAuto(q, hint, answer, reason string) {
	uiOut(uiQuestion(q, hint) + " " + answer + " " + sMuted.Render("("+reason+")"))
}

// ── Mesures de texte ───────────────────────────────────────────────────

// textWidth : largeur affichée (caractères larges et codes de couleur compris).
func textWidth(s string) int { return lipgloss.Width(s) }

// padRight : texte complété d'espaces jusqu'à la largeur (au moins rien).
func padRight(s string, width int) string {
	if n := width - textWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// truncate : texte abrégé à max caractères (« … » compris), jamais au milieu
// d'un caractère UTF-8.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// truncateMiddle : texte abrégé au milieu (« début…fin »), pour un chemin
// dont le début (préfixe, dossier) et la fin (nom de fichier) comptent.
func truncateMiddle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 3 {
		return truncate(s, max)
	}
	tail := (max - 1) * 2 / 3
	head := max - 1 - tail
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// sizeText : taille lisible (o, Ko, Mo à l'unité ; Go au dixième).
func sizeText(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d o", n)
	case n < 1024*1024:
		return fmt.Sprintf("%d Ko", n/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%d Mo", n/(1024*1024))
	}
	return strings.Replace(fmt.Sprintf("%.1f Go", float64(n)/(1024*1024*1024)), ".", ",", 1)
}

// plural : « 1 jeu », « 3 jeux ».
func plural(n int, one, many string) string {
	if n > 1 {
		return fmt.Sprintf("%d %s", n, many)
	}
	return fmt.Sprintf("%d %s", n, one)
}
