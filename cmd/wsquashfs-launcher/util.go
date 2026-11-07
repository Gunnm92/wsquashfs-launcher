package main

// Utilitaires du lanceur (script bash, section 2).

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// cleanupHook : cleanup() du lanceur, appelé une fois avant la sortie. Fixé
// avant le démarrage du gestionnaire de signaux, jamais modifié ensuite.
var cleanupHook func()

var exitOnce sync.Once

// exitNow : sortie du lanceur, avec cleanup (équivalent du trap EXIT). Une
// seule sortie : un second appel (signal pendant une sortie du thread
// principal, ou l'inverse) attend que la première termine le processus.
func exitNow(code int) {
	exitOnce.Do(func() {
		if cleanupHook != nil {
			cleanupHook()
		}
		os.Exit(code)
	})
	select {}
}

// die : affiche l'erreur (et ses lignes de détail) et quitte (code 1).
func die(msg string, details ...string) {
	fmt.Fprintln(os.Stderr, "Erreur : "+msg)
	for _, d := range details {
		fmt.Fprintln(os.Stderr, d)
	}
	exitNow(1)
}

// warn : avertissement sur la sortie d'erreur.
func warn(msg string, details ...string) {
	fmt.Fprintln(os.Stderr, "Attention : "+msg)
	for _, d := range details {
		fmt.Fprintln(os.Stderr, d)
	}
}

// usageError : rappel de syntaxe, puis code 1.
func usageError(syntax string) {
	fmt.Fprintln(os.Stderr, "Usage : "+syntax)
	exitNow(1)
}

// confirm : vrai si l'utilisateur répond oui (o/y).
func confirm(question string) bool {
	fmt.Print(question + " [o/N] ")
	reply := strings.ToLower(readLine())
	return strings.HasPrefix(reply, "o") || strings.HasPrefix(reply, "y")
}

// stdinReader : un seul tampon pour l'entrée standard : un nouveau à chaque
// question perdrait les lignes déjà lues d'avance (réponses par un tube).
var stdinReader = bufio.NewReader(os.Stdin)

// readLine : une ligne de l'entrée standard, sans fin de ligne.
func readLine() string {
	line, _ := stdinReader.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

var regexEscapeRe = regexp.MustCompile(`[\[\]\\.*^$(){}?+|/]`)

// regexEscape : échappe une chaîne pour un motif grep -E.
func regexEscape(s string) string {
	return regexEscapeRe.ReplaceAllString(s, `\$0`)
}

// unquote : retire une paire de guillemets entourant une valeur de chemin.
func unquote(v string) string {
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		return v[1 : len(v)-1]
	}
	return v
}

// runnerRoot : racine d'installation d'un binaire Wine (liens résolus).
func runnerRoot(bin string) string {
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		real = bin
	}
	return filepath.Dir(filepath.Dir(real))
}

// runnerName : nom lisible du runner d'un binaire Wine.
func runnerName(bin string) string {
	if bin == "" {
		return "aucun Wine"
	}
	if sys, err := exec.LookPath("wine"); err == nil && bin == sys {
		return "Wine système"
	}
	root := ""
	switch {
	case strings.Contains(bin, "/files/bin/"):
		root = bin[:strings.Index(bin, "/files/bin/")]
	case strings.Contains(bin, "/bin/"):
		root = bin[:strings.LastIndex(bin, "/bin/")]
	}
	base := "/"
	if root != "" {
		base = filepath.Base(root)
	}
	switch base {
	case "/", "usr", "local", "opt":
		return filepath.Base(bin) + " (" + bin + ")"
	}
	return filepath.Base(root)
}

// runnerVersion : version d'un runner (fichier version d'un Proton, sinon
// wine --version). Vide si inconnue.
func runnerVersion(bin string) string {
	if strings.HasSuffix(bin, "/files/bin/wine") {
		if data, err := os.ReadFile(strings.TrimSuffix(bin, "/files/bin/wine") + "/version"); err == nil {
			fields := strings.Fields(strings.SplitN(string(data), "\n", 2)[0])
			if len(fields) > 0 {
				return fields[len(fields)-1]
			}
			return ""
		}
	}
	// Seulement un vrai binaire wine : un script proton ne connaît pas --version.
	if !strings.HasPrefix(filepath.Base(bin), "wine") || !isExecutable(bin) {
		return ""
	}
	out, err := exec.Command("timeout", "10", bin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.SplitN(string(out), "\n", 2)[0]
}

var interpRe = regexp.MustCompile(`interpreter: (.*)\]`)

// elfInterpreterPresent : l'interpréteur ELF du binaire existe (wine 32 bits
// sur un système 64 bits pur n'a pas /lib/ld-linux.so.2).
func elfInterpreterPresent(bin string) bool {
	out, err := exec.Command("readelf", "-l", bin).Output()
	if err != nil {
		return true
	}
	m := interpRe.FindStringSubmatch(string(out))
	if m == nil || m[1] == "" {
		return true
	}
	return isExecutable(m[1])
}

// splitCommandLine : découpe à la manière de Windows : les guillemets
// regroupent et sont retirés, les antislashs restent, "" donne un argument vide.
func splitCommandLine(line string) []string {
	var args []string
	var arg strings.Builder
	inQuotes, hasArg := false, false
	for _, c := range line {
		switch {
		case c == '"':
			inQuotes = !inQuotes
			hasArg = true
		case (c == ' ' || c == '\t' || c == '\n' || c == '\r') && !inQuotes:
			if hasArg {
				args = append(args, arg.String())
			}
			arg.Reset()
			hasArg = false
		default:
			arg.WriteRune(c)
			hasArg = true
		}
	}
	if hasArg {
		args = append(args, arg.String())
	}
	return args
}

// textWidth : longueur en caractères d'un texte UTF-8 (octets de continuation
// non comptés), quelle que soit la locale.
func textWidth(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < 0x80 || s[i] > 0xBF {
			n++
		}
	}
	return n
}

// pad : texte complété d'espaces jusqu'à la largeur (au moins un espace).
func pad(text string, width int) string {
	n := width - textWidth(text)
	if n < 1 {
		n = 1
	}
	return text + strings.Repeat(" ", n)
}

// addDllOverride : ajoute une règle à WINEDLLOVERRIDES.
func addDllOverride(rule string) {
	cur := os.Getenv("WINEDLLOVERRIDES")
	if cur != "" {
		cur += ";"
	}
	os.Setenv("WINEDLLOVERRIDES", cur+rule)
}

// resolveCI : résout base/rel sans tenir compte de la casse (comme Wine).
func resolveCI(base, rel string) (string, bool) {
	cur := base
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." {
			continue
		}
		if _, err := os.Stat(cur + "/" + part); err == nil {
			cur = cur + "/" + part
			continue
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", false
		}
		match := ""
		for _, e := range entries {
			if strings.EqualFold(e.Name(), part) {
				match = cur + "/" + e.Name()
				break
			}
		}
		if match == "" {
			return "", false
		}
		cur = match
	}
	return cur, true
}
