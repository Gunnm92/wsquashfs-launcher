package main

// Commandes --pack et --install : empaquetage d'un préfixe et installation d'un jeu.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ── --pack / --install ────────────────────────────────────────────────

// packPrefix : prefix Wine ou jeu seul (dossier) → .wsquashfs (mksquashfs zstd).
func packPrefix(args []string) int {
	if len(args) == 0 || !isDirPath(args[0]) {
		usageError(os.Args[0] + " --pack <dossier.wine | dossier.pc> [sortie.wsquashfs]")
	}
	src := strings.TrimSuffix(args[0], "/")
	out := ""
	if len(args) > 1 {
		out = args[1]
	}
	if _, err := exec.LookPath("mksquashfs"); err != nil {
		die("mksquashfs absent (squashfs-tools)")
	}
	if out == "" {
		base := filepath.Base(src)
		switch lower := strings.ToLower(base); {
		case strings.HasSuffix(lower, ".wine"):
			base = base[:len(base)-5]
		case strings.HasSuffix(lower, ".pc"):
			base = base[:len(base)-3]
		}
		out = filepath.Dir(src) + "/" + base + ".wsquashfs"
	}
	saves := conf.savesDir + "/" + strings.TrimSuffix(filepath.Base(out), ".wsquashfs")
	resetSaves := false
	if pathExists(out) {
		if !isTerminal() {
			die(out + " existe déjà (jamais écrasé sans confirmation au terminal)")
		}
		st, _ := os.Stat(out)
		if !confirm(fmt.Sprintf("%s existe déjà (%s, %s). L'écraser ?", out, duHuman(out), st.ModTime().Format("02/01 15:04"))) {
			uiStatusLine(stSkip, "Abandon : rien n'a été modifié")
			return 1
		}
		if isDirPath(saves) && confirm("Réinitialiser aussi ses sauvegardes ("+saves+") ?") {
			resetSaves = true
		}
	}
	if !isFile(src + "/autorun.cmd") {
		warn("pas d'autorun.cmd dans " + src + " (lançable seulement via --exec)")
	}
	tmp := out + ".part"
	_ = os.Remove(tmp)
	uiStatusLine(stInfo, "Création de "+out+" depuis "+src+"...")
	if err := runCmd("mksquashfs", src, tmp, "-comp", "zstd", "-noappend", "-quiet"); err != nil {
		_ = os.Remove(tmp)
		die("échec de mksquashfs, " + out + " inchangé")
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		die("remplacement de " + out + " impossible : " + err.Error())
	}
	if resetSaves {
		if os.RemoveAll(saves) == nil {
			uiStatusLine(stOK, "Sauvegardes réinitialisées : "+saves)
		}
	}
	uiStatusLine(stOK, out+" ("+duHuman(out)+")")
	if v := gameVersion(src); v != "" {
		uiKV(2, kv{"Version", v})
	} else if isFile(src + "/autorun.cmd") {
		uiKV(2, kv{"Version", "non renseignée (GAME_VERSION=... dans l'autorun.cmd, voir --info)"})
	}
	return 0
}

// installGame : --install <jeu.wsquashfs> [dossier] : extraction dans
// <dossier>/<Jeu>, pour jouer sans FUSE.
func installGame(args []string) int {
	if len(args) == 0 || !isFile(args[0]) || filepath.Ext(args[0]) != ".wsquashfs" {
		usageError(os.Args[0] + " --install <jeu.wsquashfs> [dossier de destination]")
	}
	img := args[0]
	if _, err := exec.LookPath("unsquashfs"); err != nil {
		die("unsquashfs absent (squashfs-tools)")
	}
	name := strings.TrimSuffix(filepath.Base(img), ".wsquashfs")
	parent := filepath.Dir(mustAbs(img))
	if len(args) > 1 {
		parent = args[1]
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		die("impossible de créer " + parent)
	}
	dest := strings.TrimSuffix(parent, "/") + "/" + name
	if pathExists(dest) {
		die(dest + " existe déjà (jamais écrasé)")
	}
	tmp := dest + ".part"
	_ = os.RemoveAll(tmp)
	uiStatusLine(stInfo, "Installation de "+name+" dans "+dest+"...")
	if err := runCmd("unsquashfs", "-no-xattrs", "-d", tmp, img); err != nil {
		_ = os.RemoveAll(tmp)
		die("échec de l'extraction, rien n'a été installé")
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.RemoveAll(tmp)
		die("installation dans " + dest + " impossible : " + err.Error())
	}
	if dropsToTargetUser() {
		_ = runCmd("chown", "-R", fmt.Sprintf("%d:%d", conf.wineUID, conf.wineGID), dest)
	}
	uiStatusLine(stOK, dest+" ("+duHuman(dest)+")")
	pairs := []kv{}
	if v := gameVersion(dest); v != "" {
		pairs = append(pairs, kv{"Version", v})
	}
	pairs = append(pairs, kv{"Lancement", os.Args[0] + " \"" + dest + "\""})
	uiKV(2, pairs...)
	return 0
}
