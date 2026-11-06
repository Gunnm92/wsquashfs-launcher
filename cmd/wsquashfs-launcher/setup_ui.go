package main

// Affichage et questions de l'installeur (install.sh, sections 1 et 2).

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

const (
	cRed    = "\033[0;31m"
	cGreen  = "\033[0;32m"
	cYellow = "\033[1;33m"
	cBlue   = "\033[0;34m"
	cNC     = "\033[0m"
)

// État de l'installeur (install.sh, section 1).
var (
	dryRun      bool   // --dry-run : rien n'est installé ni modifié
	assumeYes   bool   // --yes : oui à toutes les questions
	agentWanted bool   // --agent : installer l'agent sans question
	installDir  string // /usr/local/bin en root, ~/.local/bin sinon
	sudoCmd     []string
	distro      string // debian | arch | autre
	distroName  string
)

func printHeader() {
	fmt.Println(cBlue + "╔════════════════════════════════════════╗" + cNC)
	fmt.Println(cBlue + "║    WSquashFS Launcher - Installation   ║" + cNC)
	fmt.Println(cBlue + "╚════════════════════════════════════════╝" + cNC)
	fmt.Println()
}

func section(title string) {
	fmt.Println()
	fmt.Println(cBlue + "── " + title + cNC)
}

func printSuccess(msg string) { fmt.Println(cGreen + "✓" + cNC + " " + msg) }
func printInfo(msg string)    { fmt.Println(cYellow + "→" + cNC + " " + msg) }
func printError(msg string)   { fmt.Fprintln(os.Stderr, cRed+"✗"+cNC+" "+msg) }

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

// askYesNo : vrai pour oui. --yes : oui ; sans terminal : la réponse par défaut.
func askYesNo(question string, def bool) bool {
	hint := "[o/N]"
	defWord := "n"
	if def {
		hint = "[O/n]"
		defWord = "o"
	}
	if assumeYes {
		fmt.Printf("  %s %s o (--yes)\n", question, hint)
		return true
	}
	if !hasTTY() {
		fmt.Printf("  %s %s %s (pas de terminal)\n", question, hint, defWord)
		return def
	}
	fmt.Printf("  %s %s ", question, hint)
	reply := strings.ToLower(strings.TrimSpace(readTTY()))
	if reply == "" {
		return def
	}
	return strings.HasPrefix(reply, "o") || strings.HasPrefix(reply, "y")
}

// askChoice : une réponse ; --yes ou sans terminal : le défaut.
func askChoice(question, def string) string {
	if assumeYes || !hasTTY() {
		fmt.Printf("  %s %s\n", question, def)
		return def
	}
	fmt.Printf("  %s ", question)
	reply := strings.TrimSpace(readTTY())
	if reply == "" {
		return def
	}
	return reply[:1]
}

// askValue : saisie sur le terminal ; le défaut avec --yes ou sans terminal.
func askValue(question, def string) string {
	if assumeYes || !hasTTY() {
		return def
	}
	fmt.Printf("%s [%s] : ", question, def)
	if answer := strings.TrimSpace(readTTY()); answer != "" {
		return answer
	}
	return def
}
