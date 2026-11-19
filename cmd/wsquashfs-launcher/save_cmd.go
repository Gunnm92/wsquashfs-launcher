package main

// Commandes --save et --saves : copie des sauvegardes de partie de l'overlay vers la ludothèque
// (saves/<système>/<jeu>/), et périmètre de ces sauvegardes (savesync, caches exclus).

import (
	"os"
	"path/filepath"
	"strconv"

	"wsquashfs-launcher/internal/savesync"
	"wsquashfs-launcher/internal/ui"
)

// ── --save et --saves (06/11) ─────────────────────────────────────────────────────

// copyGameSaves : copie les sauvegardes de partie de l'overlay dans la
// ludothèque (saves/<système>/<jeu>/), sens unique.
func copyGameSaves(name string, g ui.Games) {
	saves := conf.savesDir + "/" + name
	img := gameImage(name)
	if img == "" {
		g.Line(ui.Skip, name, "image inconnue (aucun lancement enregistré), ignoré")
		return
	}
	launch.wsquashfsFile = img
	launch.gameName = name
	dest, ok := batoceraSavesDir()
	if !ok {
		g.Line(ui.Skip, name, "hors d'un dossier roms/<système>/ (définir WSQUASHFS_BATOCERA_SAVES), ignoré")
		return
	}
	if gameUsesSaveDir(name) {
		g.Line(ui.Skip, name, "déjà dans "+dest+" (SAVEDIR/SAVEFILES)")
		return
	}
	users := saves + "/drive_c/users"
	if !isDirPath(users) {
		g.Line(ui.Skip, name, "aucune sauvegarde de partie dans l'overlay")
		return
	}
	copied, total, bytesCopied := 0, 0, int64(0)
	for _, f := range savesync.CollectSaveFiles(users) {
		total++
		src := users + "/" + f.Rel
		rel := "drive_c/users/" + f.Rel
		target := dest + "/" + rel
		// Date à la nanoseconde (savesync.CollectSaveFiles ne garde que la seconde) : copie si la source est plus récente.
		info, err := os.Lstat(src)
		if err != nil {
			ui.Warn(name + " : copie impossible de " + rel)
			continue
		}
		if tst, err := os.Stat(target); err == nil && !info.ModTime().After(tst.ModTime()) {
			continue
		}
		if os.MkdirAll(filepath.Dir(target), 0o755) == nil && copyPreserve(src, target) == nil {
			copied++
			bytesCopied += info.Size()
		} else {
			ui.Warn(name + " : copie impossible de " + rel)
		}
	}
	switch {
	case total == 0:
		g.Line(ui.Skip, name, "aucune sauvegarde de partie dans l'overlay")
	case copied > 0:
		chownToTarget(dest)
		g.Line(ui.OK, name, strconv.Itoa(copied)+" fichier(s) copié(s) ("+humanBytes(bytesCopied)+") dans "+dest)
	default:
		g.Line(ui.OK, name, "à jour ("+dest+")")
	}
}

// copyPreserve : copie d'un fichier avec date (cp -p).
func copyPreserve(src, dst string) error {
	if err := runCmd("cp", "-p", "--", src, dst); err != nil {
		return err
	}
	return nil
}

// cmdSave : --save <jeu>, les sauvegardes d'un seul jeu vers la ludothèque.
func cmdSave(args []string) int {
	name := gameNameOf(args[0])
	if !isDirPath(conf.savesDir + "/" + name) {
		return fail("aucune sauvegarde pour " + name)
	}
	return copySavesTo([]string{name})
}

// cmdSaves : --saves, les sauvegardes de tous les jeux (sans argument).
func cmdSaves() int {
	targets := savedGames()
	if len(targets) == 0 {
		ui.StatusLine(ui.Skip, "Aucune sauvegarde ("+conf.savesDir+")")
		return 0
	}
	return copySavesTo(targets)
}

// copySavesTo : copie les sauvegardes des jeux donnés (--save et --saves).
func copySavesTo(targets []string) int {
	ui.Title("Copie des sauvegardes de partie", "vers la ludothèque (saves/<système>/<jeu>/)")
	g := ui.NewGames(targets)
	for _, name := range targets {
		if gameRunning(name) {
			g.Line(ui.Skip, name, "en cours, ignoré")
			continue
		}
		copyGameSaves(name, g)
	}
	ui.Out("")
	ui.Note("Restauration (jeu arrêté) : recopier <ludothèque>/saves/<système>/<jeu>/drive_c")
	ui.Code("cp -a <ludothèque>/saves/<système>/<jeu>/drive_c " + conf.savesDir + "/<jeu>/")
	return 0
}
