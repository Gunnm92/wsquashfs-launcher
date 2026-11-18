package setup

// Complétion bash (étape de --setup et de la désinstallation).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
	if err := os.WriteFile(completionPath(), []byte(env.CompletionScript), 0o644); err != nil {
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
	if current, err := os.ReadFile(completionPath()); installed && err == nil && string(current) == env.CompletionScript {
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
