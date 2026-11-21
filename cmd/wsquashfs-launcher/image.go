package main

// Préparation des montages d'un jeu (script bash, sections 9.2 et 9.3) et
// verrous / état des jeux (section 4).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"wsquashfs-launcher/internal/savesync"
	"wsquashfs-launcher/internal/ui"
)

// ── 9.2 En-tête et chemins ─────────────────────────────────────────────

// initGamePaths : nom du jeu et chemins de ses montages et sauvegardes.
func initGamePaths() {
	launch.gameName = savesync.GameNameOf(launch.wsquashfsFile)
	launch.autorunOverride = strings.TrimSuffix(launch.wsquashfsFile, "/") + ".autorun"
	if !isFile(launch.autorunOverride) {
		launch.autorunOverride = ""
	}
	launch.squashfsPoint = filepath.Join(conf.cacheDir, "mnt", launch.gameName)
	launch.winePoint = filepath.Join(conf.cacheDir, "wine", launch.gameName)
	launch.savePoint = filepath.Join(conf.savesDir, launch.gameName)
	// workdir sur le même système de fichiers que l'upperdir (13/10)
	launch.workPoint = filepath.Join(conf.savesDir, ".work", launch.gameName)
}

// resolveGamePath : GAME_ARG en wsquashfsFile (image ou dossier) ; sortie avec
// message si le chemin est invalide.
func resolveGamePath() {
	if launch.gameArg == "" {
		ui.Error("fichier .wsquashfs ou dossier de jeu requis")
		usageError(os.Args[0] + " <fichier.wsquashfs | dossier du jeu>  |  " + os.Args[0] + " --help")
	}
	abs, err := filepath.Abs(launch.gameArg)
	if err != nil {
		die("chemin introuvable : " + launch.gameArg)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	launch.wsquashfsFile = abs
	// « .wsquashfs » ou « ..wsquashfs » : le dossier des sauvegardes du jeu
	// serait celui de tous les jeux.
	if n := savesync.GameNameOf(abs); n == "" || n == "." || n == ".." {
		die("nom de jeu invalide : " + filepath.Base(abs))
	}
	st, err := os.Stat(abs)
	switch {
	case err == nil && st.IsDir():
		// Dossier sans autorun.cmd (jeu brut, sans suffixe .pc) : lancé comme un
		// dossier .pc, en overlay ; l'exécutable est cherché par ensureFolderCmd.
		sansAutorun := !isFile(abs+"/autorun.cmd") && launch.action != "exec"
		if sansAutorun || strings.HasSuffix(strings.ToLower(abs), ".pc") {
			if strings.ContainsAny(abs, ",:") {
				die("virgule ou deux-points dans " + abs + " : impossible en overlay")
			}
			launch.folderOverlay = true
		} else {
			if launch.action == "mount" {
				die("--mount sert aux .wsquashfs et aux dossiers .pc ; un dossier installé se modifie directement")
			}
			launch.dirMode = true
		}
	case filepath.Ext(abs) != ".wsquashfs":
		die("extension .wsquashfs attendue (ou dossier de jeu installé)")
	case !isFile(abs):
		die("fichier introuvable : " + abs)
	}
}

// checkLauncherDeps : outils nécessaires au mode de montage.
func checkLauncherDeps() {
	_, wineErr := exec.LookPath("wine")
	if wineErr != nil && findWineBinary() == "" && !(findUmuRun() != "" && findProtonRunner("") != "") {
		die("aucun binaire Wine ni Proton (avec umu-run) trouvé")
	}
	if launch.headless {
		if _, err := exec.LookPath("gamescope"); err != nil {
			die("--headless demande gamescope (absent)")
		}
	}
	if launch.folderOverlay {
		if _, err := exec.LookPath("fuse-overlayfs"); err != nil {
			die("fuse-overlayfs requis pour essayer un dossier de jeu sans le modifier",
				"  → renommer le dossier en .wine pour le lancer sur place (il sera modifié)")
		}
	}
}

// ── 9.3 Montage ────────────────────────────────────────────────────────

// useInstalledDir : jeu installé, le dossier est lui-même le prefix.
func useInstalledDir() {
	launch.winePoint = launch.wsquashfsFile
	launch.squashfsPoint, launch.savePoint, launch.workPoint = "", "", ""
	if prefixInUse(regexEscape(launch.winePoint)) {
		die(launch.gameName + " est déjà lancé")
	}
	unmountAllStale()
	if isFile(launch.winePoint + "/system.reg") {
		launch.packagedPrefix = true
	}
	if isFile(launch.winePoint + "/config_info") {
		launch.protonPrefix = true
	}
	_ = os.Remove(launch.winePoint + "/.wineserver-lock")
	matches, _ := filepath.Glob(launch.winePoint + "/reg*.tmp")
	for _, m := range matches {
		_ = os.Remove(m)
	}
	launch.mountMode = "dir"
	ui.KVLine("Mode", "dossier (jeu installé, sans FUSE)")
}

// prepareMountDirs : dossiers de montage vides et au bon propriétaire.
func prepareMountDirs() {
	for _, d := range []string{launch.squashfsPoint, launch.savePoint, launch.winePoint, filepath.Join(conf.savesDir, ".images")} {
		_ = os.MkdirAll(d, 0o755)
	}
	// Image d'origine des sauvegardes, pour --clean (fichiers identiques).
	imgDir := filepath.Join(conf.savesDir, ".images")
	if err := os.WriteFile(filepath.Join(imgDir, launch.gameName), []byte(launch.wsquashfsFile+"\n"), 0o644); err != nil {
		ui.Warn("image d'origine des sauvegardes non écrite : " + err.Error())
	}
	chownToTarget(imgDir)
	resetWorkdir(launch.workPoint)
	for _, p := range []string{launch.squashfsPoint, launch.savePoint, launch.winePoint, launch.workPoint} {
		chownToTarget(p)
	}
	_ = os.Remove(launch.savePoint + "/wineserver")
	_ = os.Remove(launch.savePoint + "/.wineserver-lock")
	matches, _ := filepath.Glob(launch.savePoint + "/reg*.tmp")
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// detectImagePrefix : l'image est un prefix packagé (system.reg) ou un prefix
// créé par Proton (config_info), lu directement dans l'image.
func detectImagePrefix() {
	if launch.folderOverlay {
		if isFile(launch.wsquashfsFile + "/system.reg") {
			launch.packagedPrefix = true
		}
		if isFile(launch.wsquashfsFile + "/config_info") {
			launch.protonPrefix = true
		}
		return
	}
	if execQuiet("unsquashfs", "-cat", launch.wsquashfsFile, "system.reg") == nil {
		launch.packagedPrefix = true
	}
	if execQuiet("unsquashfs", "-cat", launch.wsquashfsFile, "config_info") == nil {
		launch.protonPrefix = true
	}
}

// clearGeneratedSystemDlls : le system32 d'un prefix packagé ne contient que
// des liens régénérés à chaque lancement (jamais pour un jeu seul).
func clearGeneratedSystemDlls() {
	if !launch.packagedPrefix {
		return
	}
	dirs := []string{launch.savePoint + "/drive_c/windows/system32", launch.savePoint + "/drive_c/windows/syswow64"}
	for _, d := range dirs {
		if launch.protonPrefix {
			entries, _ := os.ReadDir(d)
			for _, e := range entries {
				if e.Type()&os.ModeSymlink != 0 {
					_ = os.Remove(filepath.Join(d, e.Name()))
				}
			}
		} else {
			_ = os.RemoveAll(d)
		}
	}
}

// tryOverlayMount : overlay (noyau ou fuse-overlayfs). Faux si l'un manque.
func tryOverlayMount() bool {
	if launch.folderOverlay {
		if !mountOverlay(launch.wsquashfsFile) {
			return false
		}
		launch.mountMode = "overlay"
		launch.overlayKind = "fuse"
		mounted.Store(true)
		ui.KVLine("Mode", "overlay (dossier .pc + fuse-overlayfs, dossier non modifié)")
		return true
	}
	if !mountSquashfs(launch.wsquashfsFile, launch.squashfsPoint) {
		return false
	}
	if _, err := exec.LookPath("fuse-overlayfs"); err == nil && mountOverlay("") {
		launch.mountMode = "overlay"
		launch.overlayKind = "fuse"
		mounted.Store(true)
		ui.KVLine("Mode", "overlay (image + fuse-overlayfs)")
		return true
	}
	fuseUmount(launch.squashfsPoint)
	return false
}

// imageSignature : identité de l'image (chemin, taille, date) : valide le
// cache de la copie.
func imageSignature() string {
	st, err := os.Stat(launch.wsquashfsFile)
	if err != nil {
		return launch.wsquashfsFile + ":"
	}
	return fmt.Sprintf("%s:%d:%d", launch.wsquashfsFile, st.Size(), st.ModTime().Unix())
}

// extractImage : mode copy, extraction dans winePoint (image montée, puis
// unsquashfs sans FUSE).
func extractImage() {
	if mountSquashfs(launch.wsquashfsFile, launch.squashfsPoint) {
		ui.KVLine("Mode", "copy (image montée)")
		removeCopy()
		_ = os.MkdirAll(launch.winePoint, 0o755)
		if err := runCmd("cp", "-a", launch.squashfsPoint+"/.", launch.winePoint+"/"); err != nil {
			die("échec de la copie depuis l'image montée")
		}
		fuseUmount(launch.squashfsPoint)
	} else if _, err := exec.LookPath("unsquashfs"); err == nil {
		ui.KVLine("Mode", "copy (unsquashfs)")
		removeCopy()
		if err := runCmd("unsquashfs", "-d", launch.winePoint, launch.wsquashfsFile); err != nil {
			die("échec de l'extraction unsquashfs")
		}
	} else {
		die("FUSE bloqué et unsquashfs absent",
			"  → Exposer /dev/fuse dans le conteneur : --device /dev/fuse",
			"  → Ou installer squashfs-tools : apt-get install squashfs-tools")
	}
	restoreCopyUsers()
	if err := os.WriteFile(launch.winePoint+"/.wsquashfs_cache", []byte(imageSignature()), 0o644); err != nil {
		ui.Warn("signature de l'image non écrite : " + err.Error())
	}
	chownToTarget(launch.winePoint)
}

// copyUsersStash : drive_c/users d'une copie de travail (mode copy), mis de
// côté pendant que la copie est supprimée ou refaite. Hors de wine/ : ce
// dossier-là est lu comme une liste de jeux.
func copyUsersStash(name string) string {
	return filepath.Join(conf.cacheDir, "copy-users", name)
}

// stashCopyUsers : met de côté drive_c/users d'une copie de travail complète
// (marquée .wsquashfs_cache), avant sa suppression. Une mise de côté plus
// ancienne, jamais remise en place, est complétée (le plus récent l'emporte).
// Faux si les sauvegardes n'ont pas pu être mises à l'abri.
func stashCopyUsers(copyDir, name string) bool {
	users := copyDir + "/drive_c/users"
	if !isFile(copyDir+"/.wsquashfs_cache") || !isDirPath(users) {
		return true
	}
	stash := copyUsersStash(name)
	if isDirPath(stash) {
		return execQuiet("cp", "-a", users+"/.", stash+"/") == nil
	}
	if err := os.MkdirAll(filepath.Dir(stash), 0o755); err != nil {
		return false
	}
	chownToTarget(filepath.Dir(stash))
	return os.Rename(users, stash) == nil
}

// restoreCopyUsers : remet drive_c/users dans la copie de travail neuve, à la
// place de celui de l'image (comme en overlay, où les sauvegardes masquent
// l'image ; une fusion buterait sur les liens /userdata de l'image, devenus
// des dossiers). En cas d'échec, la mise de côté est gardée.
func restoreCopyUsers() {
	stash := copyUsersStash(launch.gameName)
	if !isDirPath(stash) {
		return
	}
	users := launch.winePoint + "/drive_c/users"
	if os.MkdirAll(filepath.Dir(users), 0o755) != nil || os.RemoveAll(users) != nil || os.Rename(stash, users) != nil {
		ui.Warn("sauvegardes de la copie non remises en place, gardées dans " + stash)
		return
	}
	ui.KVLine("Sauvegardes", "drive_c/users repris de la copie précédente")
}

// removeCopy : supprime l'ancienne copie de travail (drive_c/users mis de
// côté d'abord, remis par restoreCopyUsers après l'extraction). Jamais à travers un
// montage resté en place (démontage raté) : la suppression atteindrait
// l'image ou, en overlay, les sauvegardes.
func removeCopy() {
	if isMountPoint(launch.winePoint) {
		fuseUmount(launch.winePoint)
		if isMountPoint(launch.winePoint) {
			die(launch.winePoint+" est encore monté (démontage impossible) : copie annulée",
				"  → "+os.Args[0]+" --umount \""+launch.wsquashfsFile+"\"")
		}
	}
	if !stashCopyUsers(launch.winePoint, launch.gameName) {
		die("sauvegardes de la copie de travail impossibles à mettre de côté : copie gardée",
			"  ("+launch.winePoint+"/drive_c/users → "+copyUsersStash(launch.gameName)+")")
	}
	_ = os.RemoveAll(launch.winePoint)
}

// useCopyMode : copie de travail en cache, réextraite si l'image a changé.
func useCopyMode() {
	cacheInfo := launch.winePoint + "/.wsquashfs_cache"
	if data, err := os.ReadFile(cacheInfo); err == nil && string(data) == imageSignature() {
		if st, err := os.Stat(launch.winePoint); err == nil && st.IsDir() {
			ui.KVLine("Mode", "copy (cache valide, extraction ignorée)")
			launch.mountMode = "copy"
			mounted.Store(true)
			return
		}
	}
	extractImage()
	launch.mountMode = "copy"
	mounted.Store(true)
}

// mountImage : préparation et montage du jeu (overlay, sinon copie).
func mountImage() {
	if gameInUse(launch.gameName) {
		die(launch.gameName + " est déjà lancé")
	}
	unmountAllStale()
	prepareMountDirs()
	detectImagePrefix()
	clearGeneratedSystemDlls()
	runInKernelOverlay()
	if launch.folderOverlay {
		// Jamais de copie : le dossier entier serait recopié.
		if !tryOverlayMountGated() {
			die("montage fuse-overlayfs impossible pour "+launch.wsquashfsFile,
				"  → renommer le dossier en .wine pour le lancer sur place (il sera modifié)")
		}
		return
	}
	if !tryOverlayMountGated() {
		useCopyMode()
	}
}

// tryOverlayMountGated : tryOverlayMount sans signal traité en cours de
// route (cleanup verrait un montage fait mais pas encore marqué).
func tryOverlayMountGated() bool {
	signalGate.Lock()
	defer signalGate.Unlock()
	return tryOverlayMount()
}

// reportMountAndExit : --mount laisse le jeu monté pour une modification à la
// main.
func reportMountAndExit() {
	mounted.Store(false)
	ui.Section("Montage")
	pairs := []ui.Pair{{"Monté dans", launch.winePoint}}
	if launch.mountMode == "overlay" {
		kind := "le .wsquashfs"
		if launch.folderOverlay {
			kind = "le dossier"
		}
		pairs = append(pairs, ui.Pair{"Modifications", "enregistrées dans " + launch.savePoint + " (" + kind + " n'est jamais modifié)"})
	}
	pairs = append(pairs, ui.Pair{"Démontage", ""})
	ui.KV(2, pairs...)
	ui.Code(os.Args[0] + " --umount \"" + launch.wsquashfsFile + "\"")
	ui.Note("(tout lancement d'un autre jeu démonte aussi les montages inutilisés)")
	exitNow(0)
}

// ── Verrous et état des jeux (section 4) ───────────────────────────────

// prefixInUse : un processus tourne dans un prefix dont le chemin correspond
// au motif (grep -E sur WINEPREFIX= de /proc/*/environ).
func prefixInUse(pattern string) bool {
	re, err := regexp.Compile("^WINEPREFIX=" + pattern + "(/.*)?$")
	if err != nil {
		return false
	}
	matches, _ := filepath.Glob("/proc/[0-9]*/environ")
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		for _, kv := range strings.Split(string(data), "\x00") {
			if re.MatchString(kv) {
				return true
			}
		}
	}
	return false
}

// gamesPrefixRegex : dossier des prefixes de jeux (CACHE_DIR, liens résolus,
// et …/wsquashfs par défaut pour un jeu lancé avec un autre CACHE_DIR).
func gamesPrefixRegex() string {
	raw := regexEscape(conf.cacheDir)
	real := conf.cacheDir
	if r, err := filepath.EvalSymlinks(conf.cacheDir); err == nil {
		real = r
	}
	return "(" + raw + "|" + regexEscape(real) + "|.*/wsquashfs)/wine"
}

// gameInUse : le prefix de ce jeu est utilisé par un processus.
func gameInUse(name string) bool {
	return prefixInUse(gamesPrefixRegex() + "/" + regexEscape(name))
}

// gameRunning : le jeu tourne, ou un autre lancement le prépare (verrou tenu).
func gameRunning(name string) bool {
	return gameInUse(name) || gameLocked(name)
}

// savesBusy : le jeu est monté ou en cours : ses sauvegardes ne sont pas
// touchées.
func savesBusy(name string) bool {
	return gameRunning(name) || isMountPoint(filepath.Join(conf.cacheDir, "wine", name))
}

func lockPath(name string) string {
	return filepath.Join(conf.cacheDir, "locks", name+".lock")
}

// acquireGameLock : verrou du jeu (flock). Faux si déjà tenu.
func acquireGameLock() bool {
	file := lockPath(launch.gameName)
	_ = os.MkdirAll(filepath.Join(conf.cacheDir, "locks"), 0o755)
	chownToTarget(filepath.Join(conf.cacheDir, "locks"))
	if f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		f.Close()
	}
	f, err := os.Open(file)
	if err != nil {
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	launch.lockFile = f
	launch.lockedGame = launch.gameName
	return true
}

// lockIdleGame : verrou d'un jeu arrêté, le temps d'une maintenance de ses
// sauvegardes ou de son cache : un lancement simultané attend son tour (il
// répond « déjà lancé ») au lieu de monter un overlay sur des fichiers en
// cours de suppression. Faux si le jeu tourne, est monté, ou est verrouillé.
// Le verrou (nil si le fichier ne peut pas être créé) se rend par Close.
func lockIdleGame(name string) (*os.File, bool) {
	if savesBusy(name) {
		return nil, false
	}
	_ = os.MkdirAll(filepath.Join(conf.cacheDir, "locks"), 0o755)
	chownToTarget(filepath.Join(conf.cacheDir, "locks"))
	f, err := os.OpenFile(lockPath(name), os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		return nil, true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	// Lancé entre le contrôle et la prise du verrou.
	if gameInUse(name) || isMountPoint(filepath.Join(conf.cacheDir, "wine", name)) {
		f.Close()
		return nil, false
	}
	return f, true
}

// gameLocked : un AUTRE lancement tient le verrou de ce jeu.
func gameLocked(name string) bool {
	if name == launch.lockedGame {
		return false
	}
	f, err := os.Open(lockPath(name))
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// unmountAllStale : démonte les montages abandonnés sous cacheDir, sauf les
// jeux en cours (27/09).
func unmountAllStale() {
	realCache := conf.cacheDir
	if r, err := filepath.EvalSymlinks(conf.cacheDir); err == nil {
		realCache = r
	}
	for _, sub := range []string{"wine", "mnt"} {
		prefix := filepath.Join(realCache, sub) + "/"
		for _, m := range mountedPaths() {
			if strings.HasPrefix(m, prefix) && !gameRunning(filepath.Base(m)) {
				fuseUmount(m)
			}
		}
	}
}

// umountGame : arrête Wine et démonte un jeu (--umount).
func umountGame(name string) {
	if pid, ok := nsLauncherPID(name); ok {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		for i := 0; i < 30; i++ {
			if syscall.Kill(pid, 0) != nil {
				ui.StatusLine(ui.OK, "Arrêté et démonté : "+name)
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		ui.Warn(fmt.Sprintf("%s toujours en cours après 15 s (lanceur %d), non démonté", name, pid))
		exitNow(1)
	}
	prefix := filepath.Join(conf.cacheDir, "wine", name)
	if st, err := os.Stat(prefix); err != nil || !st.IsDir() {
		return
	}
	runWineserver(prefix, "-k")
	umountRetry(prefix)
	umountRetry(filepath.Join(conf.cacheDir, "mnt", name))
	ui.StatusLine(ui.OK, "Démonté : "+name)
}
