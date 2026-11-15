package main

// Complétion bash de la ligne de commande. --setup installe un petit script (completionScript) chargé
// par ~/.bashrc ; à chaque Tab, bash appelle « wsquashfs-launcher --complete <position> <ligne> »
// (commande cachée) et reçoit les remplacements du mot courant, un par ligne, déjà échappés. Une
// réponse vide laisse bash compléter les noms de fichiers (images .wsquashfs, dossiers).
//
// Ce qui se complète : les commandes (--…), les jeux qui ont des sauvegardes sur cette machine
// (--sync, --save, --clear, --erase ; avec les dossiers déclarés et les jeux Switch pour --sync), les
// mots-clés des commandes (status, dir, forget, reset…), les machines associées (--sync forget,
// --pair forget), les dossiers déclarés (--sync dir forget) et les runners (--wine). Seulement ce qui
// est sur cette machine : rien n'est demandé au réseau pendant un Tab.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// completionScript : fonction de complétion bash (installée par --setup).
const completionScript = `# Complétion bash de wsquashfs-launcher (installée par « wsquashfs-launcher --setup »)
_wsquashfs_launcher() {
	mapfile -t COMPREPLY < <("${COMP_WORDS[0]}" --complete "$COMP_POINT" "$COMP_LINE" 2>/dev/null)
}
complete -o default -F _wsquashfs_launcher wsquashfs-launcher
`

// completionCommands : commandes proposées après « wsquashfs-launcher -… ».
var completionCommands = []string{
	"--agent", "--clean", "--clear", "--doctor", "--erase", "--exec", "--headless", "--help", "--info",
	"--install", "--list", "--mount", "--pack", "--pair", "--prepare", "--save", "--saves", "--set",
	"--setup", "--show", "--sync", "--trial", "--umount", "--update", "--wine",
}

// compBreaks : séparateurs de mots de bash pour la complétion (COMP_WORDBREAKS sans les blancs).
const compBreaks = "\"'><=;|&(:"

// compLine : découpage de la ligne jusqu'au curseur.
type compLine struct {
	words     []string // mots complets (valeur, guillemets et échappements retirés), programme compris
	cur       string   // valeur du mot en cours
	prefixLen int      // octets de cur déjà avant le début du mot vu par bash (après un séparateur)
	quoted    bool     // le mot en cours est dans des guillemets ouverts : réponses sans échappement
}

// splitCompLine : découpe la ligne comme le shell (blancs, guillemets, barres obliques inverses), et
// repère où bash fait commencer le mot courant (dernier séparateur non protégé, ou guillemet ouvert).
func splitCompLine(line string) compLine {
	var c compLine
	var val strings.Builder
	started, escaped := false, false
	var quote rune
	for _, r := range line {
		switch {
		case escaped:
			val.WriteRune(r)
			escaped = false
		case quote != 0 && r == quote:
			quote = 0
		case quote == '\'':
			val.WriteRune(r)
		case r == '\\':
			escaped, started = true, true
		case quote == 0 && (r == '"' || r == '\''):
			quote, started = r, true
			c.prefixLen = val.Len()
		case quote == 0 && (r == ' ' || r == '\t' || r == '\n'):
			if started {
				c.words = append(c.words, val.String())
				val.Reset()
				started = false
			}
			c.prefixLen = 0
		default:
			val.WriteRune(r)
			started = true
			if quote == 0 && strings.ContainsRune(compBreaks, r) {
				c.prefixLen = val.Len()
			}
		}
	}
	c.cur = val.String()
	c.quoted = quote != 0
	return c
}

// compEscape : échappe pour bash ce qui n'est pas un caractère sûr (lettres, chiffres, ._+-/@%,=^:,
// et tout caractère non ASCII).
func compEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < utf8.RuneSelf && !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._+-/@%,=^:", r)) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// completeWords : candidats (valeurs complètes) pour le mot cur, après les mots args (programme exclu).
func completeWords(args []string, cur string) []string {
	if len(args) > 0 && args[len(args)-1] == "--wine" {
		return runnerNames()
	}
	cmd, rest := "", []string(nil)
	for i, a := range args {
		if a == "--wine" || a == "--set" {
			continue
		}
		if i > 0 && (args[i-1] == "--wine" || args[i-1] == "--set") {
			continue
		}
		if strings.HasPrefix(a, "--") {
			cmd, rest = a, args[i+1:]
			break
		}
	}
	if cmd == "" {
		if strings.HasPrefix(cur, "-") {
			return completionCommands
		}
		return nil // un jeu à lancer : noms de fichiers
	}
	switch cmd {
	case "--sync":
		return completeSync(rest)
	case "--save", "--clear", "--erase":
		if len(rest) == 0 {
			return savedGames()
		}
	case "--pair":
		if len(rest) > 0 && rest[len(rest)-1] == "forget" {
			return agentNames()
		}
		if len(rest) > 0 && (rest[len(rest)-1] == "roms" || rest[len(rest)-1] == "port") {
			return nil
		}
		if len(rest) == 0 {
			return []string{"forget", "port", "reset", "roms"}
		}
		return []string{"port", "roms"}
	case "--agent":
		switch {
		case len(rest) == 0:
			return []string{"run", "status", "stop"}
		case len(rest) == 1 && rest[0] == "run":
			return []string{"log"}
		}
	case "--doctor":
		if len(rest) == 0 {
			return []string{"json"}
		}
	case "--setup":
		return []string{"dry-run", "help", "uninstall", "yes"}
	case "--trial":
		if len(rest) >= 1 && rest[len(rest)-1] != "session" && rest[len(rest)-1] != "screenshot" {
			return []string{"screenshot", "session"}
		}
	case "--show":
		if len(rest) >= 1 && rest[len(rest)-1] != "--set" {
			return []string{"--set"}
		}
	}
	return nil
}

// completeSync : --sync [jeu | status [jeu] | forget <machine> | dir [add <nom> <chemin> | forget <nom>]].
func completeSync(rest []string) []string {
	switch {
	case len(rest) == 0:
		return append([]string{"dir", "forget", "status"}, syncNames()...)
	case len(rest) == 1 && rest[0] == "status":
		return syncNames()
	case len(rest) == 1 && rest[0] == "forget":
		var out []string
		for _, p := range loadSyncConfig().Peers {
			out = append(out, p.Name)
		}
		return out
	case len(rest) == 1 && rest[0] == "dir":
		return []string{"add", "forget"}
	case len(rest) == 2 && rest[0] == "dir" && rest[1] == "forget":
		var out []string
		for _, d := range loadSyncConfig().Dirs {
			out = append(out, d.Name)
		}
		return out
	}
	return nil
}

// agentNames : noms des machines associées à l'agent (--pair forget).
func agentNames() []string {
	var out []string
	for _, m := range loadAgentConfig().Machines {
		out = append(out, m.Name)
	}
	return out
}

// runnerNames : runners installés (--wine), par nom de dossier, plus « system ».
func runnerNames() []string {
	out := []string{"system"}
	for _, d := range listWineRunners() {
		out = append(out, filepath.Base(d))
	}
	return out
}

// completeLine : remplacements du mot courant pour bash (déjà échappés), triés, sans doublon.
func completeLine(line string) []string {
	c := splitCompLine(line)
	var args []string
	if len(c.words) > 0 {
		args = c.words[1:]
	}
	seen := map[string]bool{}
	var out []string
	for _, cand := range completeWords(args, c.cur) {
		if len(cand) < len(c.cur) || !strings.EqualFold(cand[:len(c.cur)], c.cur) || len(cand) < c.prefixLen {
			continue
		}
		tail := cand[c.prefixLen:]
		if !c.quoted {
			tail = compEscape(tail)
		}
		if !seen[tail] {
			seen[tail] = true
			out = append(out, tail)
		}
	}
	sort.Strings(out)
	return out
}

// cmdComplete : --complete <position> <ligne> (commande cachée, appelée par la complétion bash).
func cmdComplete(args []string) int {
	if len(args) != 2 {
		return 1
	}
	if savesDir == "" { // dossiers de initConfig, sans le reste (rien ne doit bouger pendant un Tab)
		savesDir = envOr("WSQUASHFS_SAVES_DIR", filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/saves"))
		cacheDir = envOr("WSQUASHFS_CACHE", filepath.Join(os.Getenv("HOME"), ".cache/wsquashfs"))
	}
	line := args[1]
	if pos, err := strconv.Atoi(args[0]); err == nil && pos >= 0 && pos < utf8.RuneCountInString(line) {
		line = string([]rune(line)[:pos])
	}
	for _, s := range completeLine(line) {
		fmt.Println(s)
	}
	return 0
}

// completionPath : fichier de la complétion bash installée par --setup.
func completionPath() string {
	return filepath.Join(wsqData, "completion.bash")
}

// completionSourceLine : ligne ajoutée à ~/.bashrc pour charger la complétion.
func completionSourceLine() string {
	return `[ -f "` + completionPath() + `" ] && . "` + completionPath() + `"`
}

// installCompletion : écrit le script de complétion et le charge depuis ~/.bashrc (bash seulement).
// Retourne ce qui a été fait, pour le rapport de --setup.
func installCompletion() (string, error) {
	if err := os.MkdirAll(filepath.Dir(completionPath()), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(completionPath(), []byte(completionScript), 0o644); err != nil {
		return "", err
	}
	rc := filepath.Join(os.Getenv("HOME"), ".bashrc")
	data, _ := os.ReadFile(rc)
	if strings.Contains(string(data), completionSourceLine()) {
		return "complétion bash à jour (" + completionPath() + ")", nil
	}
	f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n# WSquashFS Launcher : complétion\n%s\n", completionSourceLine()); err != nil {
		return "", err
	}
	return "complétion bash installée (" + rc + ", nouveau terminal pour en profiter)", nil
}

// removeCompletion : retire le script et sa ligne de ~/.bashrc (--setup uninstall).
func removeCompletion() bool {
	removed := false
	if isFile(completionPath()) && os.Remove(completionPath()) == nil {
		removed = true
	}
	rc := filepath.Join(os.Getenv("HOME"), ".bashrc")
	data, err := os.ReadFile(rc)
	if err != nil {
		return removed
	}
	text := string(data)
	block := "\n# WSquashFS Launcher : complétion\n" + completionSourceLine() + "\n"
	if strings.Contains(text, block) {
		text = strings.Replace(text, block, "\n", 1)
	} else if strings.Contains(text, completionSourceLine()+"\n") {
		text = strings.Replace(text, completionSourceLine()+"\n", "", 1)
	} else {
		return removed
	}
	if os.WriteFile(rc, []byte(text), 0o644) == nil {
		removed = true
	}
	return removed
}

// checkCompletion : étape de --setup. Bash seulement (le shell de SteamOS et de Batocera) ; script mis à
// jour sans question s'il est déjà chargé, sinon proposé (oui par défaut), comme le PATH.
func checkCompletion() {
	if filepath.Base(os.Getenv("SHELL")) != "bash" && !isFile(filepath.Join(os.Getenv("HOME"), ".bashrc")) {
		setupSkip("complétion : shell autre que bash, non installée")
		return
	}
	rc, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".bashrc"))
	installed := strings.Contains(string(rc), completionSourceLine())
	if current, err := os.ReadFile(completionPath()); installed && err == nil && string(current) == completionScript {
		setupHave("complétion bash (Tab) installée")
		return
	}
	if !installed && !askYesNo("Installer la complétion bash (Tab : commandes, jeux, machines) ?", true) {
		setupSkip("complétion bash non installée")
		return
	}
	if dryRun {
		simLine("écrire " + completionPath() + " et le charger depuis ~/.bashrc")
		return
	}
	msg, err := installCompletion()
	if err != nil {
		setupFail("complétion bash non installée : " + err.Error())
		return
	}
	setupDone(msg)
}
