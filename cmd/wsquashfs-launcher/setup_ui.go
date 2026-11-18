package main

// Affichage et questions de l'installeur (install.sh, sections 1 et 2). Tout
// passe par internal/ui : étapes numérotées (ui.Stage), résultats (ui.Item), questions
// (ui.Prompt / ui.Auto) et bilan.

import (
	"bufio"
	"os"
	"strings"

	"wsquashfs-launcher/internal/ui"
)

// État de l'installeur (install.sh, section 1).
var (
	dryRun     bool   // --setup dry-run : rien n'est installé ni modifié
	assumeYes  bool   // --setup yes : oui à toutes les questions
	installDir string // /usr/local/bin en root, ~/.local/bin sinon
	sudoCmd    []string
	distro     string // debian | arch | autre
	distroName string
)

// Parcours : numéro de l'étape courante, nombre total (setupPlanTotal) et
// compte des résultats du bilan.
var (
	stageN     int
	stageTotal int
	setupCount struct{ installed, present, skipped, failed int }
)

// setupPlanTotal : nombre d'étapes du parcours, calculé avant le premier
// affichage. Les étapes NVIDIA et AMD existent seulement si la carte est là,
// l'agent seulement s'il est demandé ou proposé.
func setupPlanTotal() int {
	n := 8 // paquets, Wine, Proton, rendu, lanceur, association, PATH, vérification
	if hasGPU("0x10de") {
		n++
	}
	if hasGPU("0x1002") {
		n++
	}
	return n
}

// setupStage : étape suivante du parcours.
func setupStage(title string) {
	stageN++
	ui.Stage(stageN, stageTotal, title)
}

// setupDone : élément installé (ou à installer en simulation).
func setupDone(msg string, details ...string) {
	setupCount.installed++
	ui.Item(ui.OK, msg, details...)
}

// setupHave : élément déjà présent.
func setupHave(msg string, details ...string) {
	setupCount.present++
	ui.Item(ui.OK, msg, details...)
}

// setupSkip : élément ignoré (choix, facultatif absent).
func setupSkip(msg string, details ...string) {
	setupCount.skipped++
	ui.Item(ui.Skip, msg, details...)
}

// setupFail : échec d'un élément. Un échec secondaire (un téléchargement qui
// rate, suivi de l'échec de l'élément) s'affiche avec setupErr, sans compte.
func setupFail(msg string, details ...string) {
	setupCount.failed++
	ui.Item(ui.Fail, msg, details...)
}

// setupErr : constat d'échec sans compte (voir setupFail).
func setupErr(msg string, details ...string) { ui.Item(ui.Fail, msg, details...) }

// setupTodo : élément manquant, à installer.
func setupTodo(msg string, details ...string) { ui.Item(ui.Info, msg, details...) }

// simLine : ligne de simulation (rien n'est fait).
func simLine(text string) { ui.Note("[simulation] " + text) }

// doneWord : « installé(s) », ou « à installer » en simulation.
func doneWord() string {
	if dryRun {
		return "à installer (simulation)"
	}
	return "installé(s)"
}

// hasTTY : vrai si /dev/tty est accessible en lecture et en écriture. Les
// questions passent par le terminal, jamais par l'entrée standard.
func hasTTY() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// readTTY lit une ligne sur le terminal.
func readTTY() string {
	f, err := os.Open("/dev/tty")
	if err != nil {
		return ""
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

// autoReason : raison d'une réponse automatique.
func autoReason() string {
	if assumeYes {
		return "yes"
	}
	return "pas de terminal"
}

// askYesNo : vrai pour oui. yes : oui ; sans terminal : la réponse par défaut.
func askYesNo(question string, def bool) bool {
	hint := ui.YesNo(def)
	defWord := "n"
	if def {
		defWord = "o"
	}
	if assumeYes {
		ui.Auto(question, hint, "o", "yes")
		return true
	}
	if !hasTTY() {
		ui.Auto(question, hint, defWord, "pas de terminal")
		return def
	}
	ui.Prompt(question, hint)
	reply := strings.ToLower(strings.TrimSpace(readTTY()))
	if reply == "" {
		return def
	}
	return strings.HasPrefix(reply, "o") || strings.HasPrefix(reply, "y")
}

// askChoice : une réponse ; yes ou sans terminal : le défaut.
func askChoice(question, hint, def string) string {
	if assumeYes || !hasTTY() {
		ui.Auto(question, hint, def, autoReason())
		return def
	}
	ui.Prompt(question, hint)
	reply := strings.TrimSpace(readTTY())
	if reply == "" {
		return def
	}
	return reply[:1]
}
