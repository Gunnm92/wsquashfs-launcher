// Package ui : règles et fonctions d'affichage du lanceur. Tout ce qui s'affiche
// pour un humain passe par ce paquet.
//
// Règles
//  1. Le texte fait foi : symboles, mots et structure disent tout ; couleur
//     et gras ne font que les souligner. Hors terminal (tube, fichier,
//     frontend, agent, tests, NO_COLOR, TERM=dumb), aucun code de couleur :
//     le texte seul, identique.
//  2. Tout s'écrit par Out / Err (lipgloss.Fprintln : codes retirés quand
//     la sortie n'est pas un terminal). Jamais de fmt.Print d'un texte stylé,
//     jamais de style stocké dans une donnée réutilisée (JSON, comparaison).
//  3. 16 couleurs de base seulement (le thème du terminal les adapte) ; pas de
//     couleur adaptative qui interroge le terminal.
//  4. Un même type d'information a toujours le même rendu, dans toutes les
//     commandes :
//     Title    titre de la commande, encadré (un par commande)
//     Stage    étape numérotée d'un parcours (--setup)
//     Section  groupe d'éléments sous le titre
//     KV       « Clé : valeur » alignés (KVLine au fil de l'eau)
//     StatusLine  action globale : ✓ fait, ✗ échec, – ignoré, → en cours
//     Item     élément d'une section, même symboles, détails en dessous
//     Games    une ligne par jeu, noms en colonne
//     List     rubrique suivie de noms
//     Table    tableau encadré, en-tête en gras
//     Note     explication, simulation, astuce (discrète)
//     Code     texte à copier, seul sur sa ligne
//     Rule     séparateur (sortie du jeu seulement)
//     Verdict  dernière ligne d'un rapport
//     Prompt   questions (avec Auto, Menu), une seule forme
//     Warn     « Attention : » sur stderr, la commande continue
//     Error    « Erreur : » sur stderr ; « die » (main) s'y ajoute : sortie, code 1
//  5. Sorties machine hors charte : JSON (--doctor json, --trial, agent
//     HTTP) et journal de l'agent passent par encoding/json ou fmt directement.
//  6. Retraits : 0 pour titres, sections, statuts globaux, clés-valeurs au fil
//     de l'eau et verdicts ; 2 pour les éléments, listes, notes et lignes de
//     jeu (le manager filtre les lignes de --saves qui commencent par deux
//     espaces) ; 6 pour les détails d'un élément.
//  7. Ce paquet n'appelle jamais os.Exit : « die » (Error puis sortie avec
//     cleanup) vit dans le paquet main.
package ui

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

// stdout, stderr : destinations (remplacées par les tests).
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// SetOutput : remplace les destinations de Out et Err (tests des paquets qui
// affichent) ; la fonction rendue rétablit les anciennes.
func SetOutput(out, errs io.Writer) (restore func()) {
	oldOut, oldErr := stdout, stderr
	stdout, stderr = out, errs
	return func() { stdout, stderr = oldOut, oldErr }
}

// Out : une ligne sur la sortie standard (codes retirés hors terminal).
// Les espaces de fin de ligne sont retirés (tableaux, encadrés).
func Out(s string) { write(stdout, trimLines(s)+"\n") }

// Err : une ligne sur la sortie d'erreur.
func Err(s string) { write(stderr, trimLines(s)+"\n") }

// write : écriture par lipgloss (codes retirés hors terminal). NO_COLOR
// l'emporte sur tout, CLICOLOR_FORCE compris (no-color.org) : colorprofile
// v0.4.3 garde les couleurs hors terminal quand les deux sont posés.
func write(w io.Writer, s string) {
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

type Status int

const (
	OK   Status = iota // ✓ fait, présent
	Fail               // ✗ échec d'un élément (la commande continue)
	Skip               // – ignoré, optionnel, non bloquant
	Info               // → étape en cours, information neutre
)

// Mark : symbole coloré du statut.
func (st Status) Mark() string {
	switch st {
	case OK:
		return sOK.Render("✓")
	case Fail:
		return sErr.Render("✗")
	case Skip:
		return sWarn.Render("–")
	}
	return sCode.Render("→")
}

// StatusLine : action globale (retrait 0). Un message Info se termine
// par « ... » s'il décrit une action en cours (à la charge de l'appelant).
func StatusLine(st Status, msg string) { Out(st.Mark() + " " + msg) }

// Item : élément d'une section (retrait 2), détails à 6 espaces.
func Item(st Status, msg string, details ...string) {
	Out("  " + st.Mark() + " " + msg)
	for _, d := range details {
		Out("      " + sMuted.Render(d))
	}
}

// ── Titres ─────────────────────────────────────────────────────────────

// Title : titre de la commande, encadré ; sous-titre en dessous, discret.
func Title(text string, subtitle ...string) {
	body := sBold.Render(text)
	for _, s := range subtitle {
		if s != "" {
			body += "\n" + sMuted.Render(s)
		}
	}
	Out(sTitle.Render(body))
}

// Section : groupe d'éléments, précédé d'une ligne vide.
func Section(text string) {
	Out("")
	Out(sSection.Render("▍" + text))
}

// Stage : étape numérotée d'un parcours (--setup), précédée d'une ligne vide.
func Stage(n, total int, text string) {
	Out("")
	Out(sSection.Render(fmt.Sprintf("▍%d/%d  %s", n, total, text)))
}

// ── Clés-valeurs ───────────────────────────────────────────────────────

// keyWidth : colonne des clés émises au fil de l'eau (KVLine) ; la plus
// longue clé du lancement (« Réglages machine ») y tient.
const keyWidth = 16

// Pair : une paire {clé, valeur} pour KV (tableau : littéraux sans champs
// nommés, utilisables depuis les autres paquets).
type Pair [2]string

// KVLine : une paire, clé sur keyWidth colonnes (retrait 0).
func KVLine(key, value string) {
	Out(sMuted.Render(padRight(key, keyWidth)+" :") + " " + value)
}

// KV : bloc de paires alignées sur la plus longue clé.
func KV(indent int, pairs ...Pair) {
	w := 0
	for _, p := range pairs {
		w = max(w, textWidth(p[0]))
	}
	for _, p := range pairs {
		Out(strings.Repeat(" ", indent) + sMuted.Render(padRight(p[0], w)+" :") + " " + p[1])
	}
}

// ── Jeux, listes, tableaux ─────────────────────────────────────────────

// NameMax : au-delà, un nom de jeu est abrégé (« … »).
const NameMax = 48

// Games : lignes par jeu, noms alignés sur la colonne la plus large.
type Games struct{ width int }

// NewGames : mise en colonnes pour les noms donnés.
func NewGames(names []string) Games {
	w := 3
	for _, n := range names {
		w = max(w, textWidth(Truncate(n, NameMax)))
	}
	return Games{w}
}

// Line : « ␣␣✓ Nom   message » (retrait 2, contrat du manager pour --saves).
func (g Games) Line(st Status, name, msg string) {
	Out("  " + st.Mark() + " " + padRight(Truncate(name, NameMax), g.width) + "  " + msg)
}

// List : rubrique en gras avec le nombre d'éléments, puis un nom par ligne
// (retrait 2). Rien si la liste est vide.
func List(heading string, names []string) {
	if len(names) == 0 {
		return
	}
	Out("")
	Out(sBold.Render(fmt.Sprintf("%s (%d)", heading, len(names))))
	for _, n := range names {
		Out("  " + sMuted.Render("·") + " " + n)
	}
}

// Cell : style d'une cellule de tableau selon sa valeur (nil : aucun).
type Cell func(row, col int, value string) lipgloss.Style

// Table : tableau encadré, en-tête en gras ; right : colonnes alignées à
// droite (nombres, tailles).
//
// Plus large que le terminal : les colonnes de texte les plus larges sont
// rognées (« … » ; un chemin garde son début et sa fin). Hors terminal, rien
// n'est rogné : la sortie ne dépend pas de la machine.
func Table(headers []string, rows [][]string, right []int, cell Cell) {
	isRight := map[int]bool{}
	for _, c := range right {
		isRight[c] = true
	}
	rows = fitColumns(headers, rows, isRight, termWidth())
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
	Out(t.Render())
}

// termWidth : largeur du terminal de la sortie standard, 0 hors terminal.
var termWidth = func() int {
	f, ok := stdout.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) {
		return 0
	}
	w, _, err := term.GetSize(f.Fd())
	if err != nil {
		return 0
	}
	return w
}

// minColumn : largeur en dessous de laquelle une colonne n'est plus rognée.
const minColumn = 12

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
			if right[c] || w[c] <= max(minColumn, textWidth(headers[c])) {
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
					v = Truncate(v, w[c])
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

// StateCell : colonne d'état de --list (en cours, monté ou verrouillé, -)
// et colonne d'image introuvable, colorées selon la valeur.
func StateCell(stateCol, imageCol int) Cell {
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

// BoldRow : ligne de total (row, indice de données) en gras.
func BoldRow(row int) Cell {
	return func(r, _ int, _ string) lipgloss.Style {
		if r == row {
			return sBold
		}
		return cellPlain
	}
}

// ── Texte ──────────────────────────────────────────────────────────────

// Note : explication, simulation, astuce (retrait 2, discrète). Les
// lignes sont coupées par l'appelant (sortie identique sur toutes les largeurs).
func Note(lines ...string) {
	for _, l := range lines {
		Out("  " + sMuted.Render(l))
	}
}

// Code : texte à copier (code d'association, commande), seul sur sa ligne.
func Code(text string) { Out("  " + sCode.Render(text)) }

// Rule : séparateur, avec une étiquette facultative (sortie du jeu).
func Rule(label string) {
	line := strings.Repeat("─", 41)
	if label != "" {
		line += " " + label
	}
	Out(sMuted.Render(line))
}

// Verdict : dernière ligne d'un rapport, précédée d'une ligne vide.
func Verdict(ok bool, msg string) {
	Out("")
	if ok {
		Out(sOK.Render("✓ " + msg))
	} else {
		Out(sErr.Render("✗ " + msg))
	}
}

// Help : texte d'aide fixe (usage.txt) ; rubriques (ligne en colonne 0
// finissant par « : ») en gras, commandes en couleur. Hors terminal, le texte
// est rendu à l'identique.
func Help(text string) {
	for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		switch t := strings.TrimSpace(l); {
		case l != "" && l[0] != ' ' && strings.HasSuffix(l, ":"):
			Out(sBold.Render(l))
		case strings.HasPrefix(t, "wsquashfs-launcher"):
			indent := l[:len(l)-len(strings.TrimLeft(l, " "))]
			Out(indent + sCode.Render(t))
		default:
			Out(l)
		}
	}
}

// ── Messages d'erreur (stderr) ─────────────────────────────────────────

// Warn : avertissement, la commande continue.
func Warn(msg string, details ...string) {
	Err(sWarn.Render("Attention :") + " " + msg)
	for _, d := range details {
		Err(d) // retrait à la charge de l'appelant (« ␣␣→ … »)
	}
}

// Error : « Erreur : » et ses détails, sans quitter.
func Error(msg string, details ...string) {
	Err(sErr.Render("Erreur :") + " " + msg)
	for _, d := range details {
		Err(d) // retrait à la charge de l'appelant (« ␣␣→ … »)
	}
}

// Usage : rappel de syntaxe, sans quitter.
func Usage(syntax string) { Err(sErr.Render("Usage :") + " " + syntax) }

// ── Questions ──────────────────────────────────────────────────────────
//
// Une seule forme : « ␣␣? Question [o/n] (défaut : non) » ; toujours
// « o/n » dans cet ordre, la réponse par défaut écrite en toutes lettres
// (YesNo). Réponse automatique affichée
// en clair : « o (yes) », « n (pas de terminal) ».

func question(q, hint string) string {
	return "  " + sCode.Render("?") + " " + q + " " + sMuted.Render(hint)
}

// YesNo : indication d'une question oui/non, identique partout.
func YesNo(def bool) string {
	if def {
		return "[o/n] (défaut : oui)"
	}
	return "[o/n] (défaut : non)"
}

// Menu : choix numérotés (« ␣␣1  libellé »), le choix par défaut marqué ;
// suivi d'une question askChoice. def : numéro par défaut (« 2 »).
func Menu(options []string, def string) {
	for i, o := range options {
		n := fmt.Sprint(i + 1)
		line := "  " + sCode.Render(n) + "  " + o
		if n == def {
			line += "  " + sMuted.Render("(défaut)")
		}
		Out(line)
	}
}

// Prompt : question sans fin de ligne (la réponse est saisie à la suite).
func Prompt(q, hint string) {
	write(stdout, question(q, hint)+" ")
}

// Auto : question à réponse automatique, affichée avec sa raison.
func Auto(q, hint, answer, reason string) {
	Out(question(q, hint) + " " + answer + " " + sMuted.Render("("+reason+")"))
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

// Truncate : texte abrégé à max caractères (« … » compris), jamais au milieu
// d'un caractère UTF-8.
func Truncate(s string, max int) string {
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
		return Truncate(s, max)
	}
	tail := (max - 1) * 2 / 3
	head := max - 1 - tail
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// SizeText : taille lisible (o, Ko, Mo à l'unité ; Go au dixième).
func SizeText(n int64) string {
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

// Plural : « 1 jeu », « 3 jeux ».
func Plural(n int, one, many string) string {
	if n > 1 {
		return fmt.Sprintf("%d %s", n, many)
	}
	return fmt.Sprintf("%d %s", n, one)
}
