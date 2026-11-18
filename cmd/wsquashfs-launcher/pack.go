package main

// Commandes --pack et --install : empaquetage d'un préfixe et installation d'un jeu.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"wsquashfs-launcher/internal/ui"
)

// ── --pack / --install ────────────────────────────────────────────────

// imageCoverPath : illustration du jeu dans l'image (jaquette ou icône, tout
// format lu par gdk-pixbuf : PNG, JPEG, ICO, WebP), déposée par --pack (depuis
// le dossier source) ou par la reconstruction du manager.
const imageCoverPath = ".wsquashfs/cover"

// packPrefix : prefix Wine ou jeu seul (dossier) → .wsquashfs (mksquashfs zstd).
// Sous-option : « cover FICHIER » ajoute une illustration (imageCoverPath) sans
// rien écrire dans le dossier source.
func packPrefix(args []string) int {
	const syntax = "--pack <dossier.wine | dossier.pc> [sortie.wsquashfs] [cover FICHIER]"
	var pos []string
	cover, hasCover := "", false
	for i := 0; i < len(args); i++ {
		if i > 0 && args[i] == "cover" {
			if i+1 >= len(args) {
				usageError(os.Args[0] + " " + syntax)
			}
			cover, hasCover = args[i+1], true
			i++
			continue
		}
		pos = append(pos, args[i])
	}
	if len(pos) == 0 || !isDirPath(pos[0]) {
		usageError(os.Args[0] + " " + syntax)
	}
	src := strings.TrimSuffix(pos[0], "/")
	out := ""
	if len(pos) > 1 {
		out = pos[1]
	}
	if _, err := exec.LookPath("mksquashfs"); err != nil {
		die("mksquashfs absent (squashfs-tools)")
	}
	var coverArgs []string
	if hasCover {
		if !isFile(cover) {
			die("illustration introuvable (fichier attendu) : " + cover)
		}
		coverArgs = coverPseudoArgs(src, mustAbs(cover))
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
			ui.StatusLine(ui.Skip, "Abandon : rien n'a été modifié")
			return 1
		}
		if isDirPath(saves) && confirm("Réinitialiser aussi ses sauvegardes ("+saves+") ?") {
			resetSaves = true
		}
	}
	if !isFile(src + "/autorun.cmd") {
		ui.Warn("pas d'autorun.cmd dans " + src + " (lançable seulement via --exec)")
	}
	tmp := out + ".part"
	_ = os.Remove(tmp)
	ui.StatusLine(ui.Info, "Création de "+out+" depuis "+src+"...")
	mkArgs := append([]string{src, tmp, "-comp", "zstd", "-noappend", "-quiet"}, coverArgs...)
	if err := runCmd("mksquashfs", mkArgs...); err != nil {
		_ = os.Remove(tmp)
		die("échec de mksquashfs, " + out + " inchangé")
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		die("remplacement de " + out + " impossible : " + err.Error())
	}
	if resetSaves {
		if os.RemoveAll(saves) == nil {
			ui.StatusLine(ui.OK, "Sauvegardes réinitialisées : "+saves)
		}
	}
	ui.StatusLine(ui.OK, out+" ("+duHuman(out)+")")
	if hasCover {
		ui.KV(2, ui.Pair{"Illustration", mustAbs(cover)})
	}
	if v := gameVersion(src); v != "" {
		ui.KV(2, ui.Pair{"Version", v})
	} else if isFile(src + "/autorun.cmd") {
		ui.KV(2, ui.Pair{"Version", "non renseignée (GAME_VERSION=... dans l'autorun.cmd, voir --info)"})
	}
	return 0
}

// coverPseudoArgs : options pseudo de mksquashfs qui ajoutent l'illustration
// (imageCoverPath) à l'image, sans rien écrire dans le dossier source. Refus si
// le dossier source contient déjà cette illustration ou un .wsquashfs non dossier.
func coverPseudoArgs(src, cover string) []string {
	if pathExists(filepath.Join(src, imageCoverPath)) {
		die("conflit : " + src + " contient déjà " + imageCoverPath + " (sous-option cover refusée)")
	}
	uid, gid := os.Getuid(), os.Getgid()
	var args []string
	dir := filepath.Join(src, ".wsquashfs")
	if !isDirPath(dir) {
		if pathExists(dir) {
			die("conflit : " + dir + " existe et n'est pas un dossier")
		}
		args = append(args, "-p", fmt.Sprintf(".wsquashfs d 755 %d %d", uid, gid))
	}
	return append(args, "-p", fmt.Sprintf("%s f 644 %d %d cat %s", imageCoverPath, uid, gid, shQuote(cover)))
}

// shQuote : argument quoté pour /bin/sh (apostrophes internes échappées).
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
	ui.StatusLine(ui.Info, "Installation de "+name+" dans "+dest+"...")
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
	ui.StatusLine(ui.OK, dest+" ("+duHuman(dest)+")")
	pairs := []ui.Pair{}
	if v := gameVersion(dest); v != "" {
		pairs = append(pairs, ui.Pair{"Version", v})
	}
	pairs = append(pairs, ui.Pair{"Lancement", os.Args[0] + " \"" + dest + "\""})
	ui.KV(2, pairs...)
	return 0
}
