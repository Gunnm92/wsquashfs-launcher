package main

// Commandes --save et --saves : copie des sauvegardes de partie de l'overlay vers la ludothèque
// (saves/<système>/<jeu>/), et périmètre de ces sauvegardes (saveProfileDirs, caches exclus).

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ── --save et --saves (06/11) ─────────────────────────────────────────────────────

// saveProfileDirs : dossiers du profil Windows où les jeux écrivent leurs
// sauvegardes de partie.
var saveProfileDirs = []string{"Documents", "Saved Games", "AppData/Roaming", "AppData/LocalLow", "AppData/Local",
	"My Documents", "Application Data", "Local Settings/Application Data"}

// copyGameSaves : copie les sauvegardes de partie de l'overlay dans la
// ludothèque (saves/<système>/<jeu>/), sens unique.
func copyGameSaves(name string, g uiGames) {
	saves := conf.savesDir + "/" + name
	img := gameImage(name)
	if img == "" {
		g.line(stSkip, name, "image inconnue (aucun lancement enregistré), ignoré")
		return
	}
	launch.wsquashfsFile = img
	launch.gameName = name
	dest, ok := batoceraSavesDir()
	if !ok {
		g.line(stSkip, name, "hors d'un dossier roms/<système>/ (définir WSQUASHFS_BATOCERA_SAVES), ignoré")
		return
	}
	if gameUsesSaveDir(name) {
		g.line(stSkip, name, "déjà dans "+dest+" (SAVEDIR/SAVEFILES)")
		return
	}
	users := saves + "/drive_c/users"
	if !isDirPath(users) {
		g.line(stSkip, name, "aucune sauvegarde de partie dans l'overlay")
		return
	}
	copied, total, bytesCopied := 0, 0, int64(0)
	for _, f := range collectSaveFiles(users) {
		total++
		src := users + "/" + f.Rel
		rel := "drive_c/users/" + f.Rel
		target := dest + "/" + rel
		// Date à la nanoseconde (collectSaveFiles ne garde que la seconde) : copie si la source est plus récente.
		info, err := os.Lstat(src)
		if err != nil {
			warn(name + " : copie impossible de " + rel)
			continue
		}
		if tst, err := os.Stat(target); err == nil && !info.ModTime().After(tst.ModTime()) {
			continue
		}
		if os.MkdirAll(filepath.Dir(target), 0o755) == nil && copyPreserve(src, target) == nil {
			copied++
			bytesCopied += info.Size()
		} else {
			warn(name + " : copie impossible de " + rel)
		}
	}
	switch {
	case total == 0:
		g.line(stSkip, name, "aucune sauvegarde de partie dans l'overlay")
	case copied > 0:
		chownToTarget(dest)
		g.line(stOK, name, strconv.Itoa(copied)+" fichier(s) copié(s) ("+humanBytes(bytesCopied)+") dans "+dest)
	default:
		g.line(stOK, name, "à jour ("+dest+")")
	}
}

// isCacheDirName : dossiers de caches exclus des sauvegardes.
func isCacheDirName(n string) bool {
	switch n {
	case "Temp", "NVIDIA", "D3DSCache", "DXCache", "INetCache", "CrashDumps":
		return true
	}
	return strings.Contains(strings.ToLower(n), "shadercache")
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
		die("aucune sauvegarde pour " + name)
	}
	return copySavesTo([]string{name})
}

// cmdSaves : --saves, les sauvegardes de tous les jeux (sans argument).
func cmdSaves() int {
	targets := savedGames()
	if len(targets) == 0 {
		uiStatusLine(stSkip, "Aucune sauvegarde ("+conf.savesDir+")")
		return 0
	}
	return copySavesTo(targets)
}

// copySavesTo : copie les sauvegardes des jeux donnés (--save et --saves).
func copySavesTo(targets []string) int {
	uiTitle("Copie des sauvegardes de partie", "vers la ludothèque (saves/<système>/<jeu>/)")
	g := newUIGames(targets)
	for _, name := range targets {
		if gameRunning(name) {
			g.line(stSkip, name, "en cours, ignoré")
			continue
		}
		copyGameSaves(name, g)
	}
	uiOut("")
	uiNote("Restauration (jeu arrêté) : recopier <ludothèque>/saves/<système>/<jeu>/drive_c")
	uiCode("cp -a <ludothèque>/saves/<système>/<jeu>/drive_c " + conf.savesDir + "/<jeu>/")
	return 0
}
