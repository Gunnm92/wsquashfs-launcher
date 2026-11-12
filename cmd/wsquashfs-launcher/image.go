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
)

// ── 9.2 En-tête et chemins ─────────────────────────────────────────────

// initGamePaths : nom du jeu et chemins de ses montages et sauvegardes.
func initGamePaths() {
	gameName = gameNameOf(wsquashfsFile)
	autorunOverride = strings.TrimSuffix(wsquashfsFile, "/") + ".autorun"
	if !isFile(autorunOverride) {
		autorunOverride = ""
	}
	squashfsPoint = filepath.Join(cacheDir, "mnt", gameName)
	winePoint = filepath.Join(cacheDir, "wine", gameName)
	savePoint = filepath.Join(savesDir, gameName)
	// workdir sur le même système de fichiers que l'upperdir (13/10)
	workPoint = filepath.Join(savesDir, ".work", gameName)
}

// gameNameOf : nom du jeu d'après une image, un dossier ou un nom.
// Un dossier Jeu.pc a le nom de l'image qu'il donnera (Jeu.wsquashfs).
func gameNameOf(p string) string {
	n := filepath.Base(strings.TrimSuffix(p, "/"))
	switch {
	case strings.HasSuffix(n, ".wsquashfs"):
		return strings.TrimSuffix(n, ".wsquashfs")
	case strings.HasSuffix(strings.ToLower(n), ".pc"):
		return n[:len(n)-3]
	}
	return n
}

// resolveGamePath : GAME_ARG en wsquashfsFile (image ou dossier) ; sortie avec
// message si le chemin est invalide.
func resolveGamePath() {
	if gameArg == "" {
		uiError("fichier .wsquashfs ou dossier de jeu requis")
		usageError(os.Args[0] + " <fichier.wsquashfs | dossier du jeu>  |  " + os.Args[0] + " --help")
	}
	abs, err := filepath.Abs(gameArg)
	if err != nil {
		die("chemin introuvable : " + gameArg)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	wsquashfsFile = abs
	// « .wsquashfs » ou « ..wsquashfs » : le dossier des sauvegardes du jeu
	// serait celui de tous les jeux.
	if n := gameNameOf(abs); n == "" || n == "." || n == ".." {
		die("nom de jeu invalide : " + filepath.Base(abs))
	}
	st, err := os.Stat(abs)
	switch {
	case err == nil && st.IsDir():
		if !isFile(abs+"/autorun.cmd") && action != "exec" {
			die("pas d'autorun.cmd dans " + abs)
		}
		if strings.HasSuffix(strings.ToLower(abs), ".pc") {
			if strings.ContainsAny(abs, ",:") {
				die("virgule ou deux-points dans " + abs + " : impossible en overlay")
			}
			folderOverlay = true
		} else {
			if action == "mount" {
				die("--mount sert aux .wsquashfs et aux dossiers .pc ; un dossier installé se modifie directement")
			}
			dirMode = true
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
	if headless {
		if _, err := exec.LookPath("gamescope"); err != nil {
			die("--headless demande gamescope (absent)")
		}
	}
	if folderOverlay {
		if _, err := exec.LookPath("fuse-overlayfs"); err != nil {
			die("fuse-overlayfs requis pour essayer un dossier .pc sans le modifier",
				"  → renommer le dossier en .wine pour le lancer sur place (il sera modifié)")
		}
	} else if !dirMode {
		_, e1 := exec.LookPath("squashfuse")
		_, e2 := exec.LookPath("unsquashfs")
		if e1 != nil && e2 != nil {
			die("ni squashfuse ni unsquashfs ne sont disponibles",
				"  → apt-get install squashfuse squashfs-tools")
		}
	}
}

// ── 9.3 Montage ────────────────────────────────────────────────────────

// useInstalledDir : jeu installé, le dossier est lui-même le prefix.
func useInstalledDir() {
	winePoint = wsquashfsFile
	squashfsPoint, savePoint, workPoint = "", "", ""
	if prefixInUse(regexEscape(winePoint)) {
		die(gameName + " est déjà lancé")
	}
	unmountAllStale()
	if isFile(winePoint + "/system.reg") {
		packagedPrefix = true
	}
	if isFile(winePoint + "/config_info") {
		protonPrefix = true
	}
	_ = os.Remove(winePoint + "/.wineserver-lock")
	matches, _ := filepath.Glob(winePoint + "/reg*.tmp")
	for _, m := range matches {
		_ = os.Remove(m)
	}
	mountMode = "dir"
	uiKVLine("Mode", "dossier (jeu installé, sans FUSE)")
}

// prepareMountDirs : dossiers de montage vides et au bon propriétaire.
func prepareMountDirs() {
	for _, d := range []string{squashfsPoint, savePoint, winePoint, filepath.Join(savesDir, ".images")} {
		_ = os.MkdirAll(d, 0o755)
	}
	// Image d'origine des sauvegardes, pour --clean (fichiers identiques).
	imgDir := filepath.Join(savesDir, ".images")
	_ = os.WriteFile(filepath.Join(imgDir, gameName), []byte(wsquashfsFile+"\n"), 0o644)
	chownToTarget(imgDir)
	resetWorkdir(workPoint)
	for _, p := range []string{squashfsPoint, savePoint, winePoint, workPoint} {
		chownToTarget(p)
	}
	_ = os.Remove(savePoint + "/wineserver")
	_ = os.Remove(savePoint + "/.wineserver-lock")
	matches, _ := filepath.Glob(savePoint + "/reg*.tmp")
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

// detectImagePrefix : l'image est un prefix packagé (system.reg) ou un prefix
// créé par Proton (config_info), lu directement dans l'image.
func detectImagePrefix() {
	if folderOverlay {
		if isFile(wsquashfsFile + "/system.reg") {
			packagedPrefix = true
		}
		if isFile(wsquashfsFile + "/config_info") {
			protonPrefix = true
		}
		return
	}
	if execQuiet("unsquashfs", "-cat", wsquashfsFile, "system.reg") == nil {
		packagedPrefix = true
	}
	if execQuiet("unsquashfs", "-cat", wsquashfsFile, "config_info") == nil {
		protonPrefix = true
	}
}

// clearGeneratedSystemDlls : le system32 d'un prefix packagé ne contient que
// des liens régénérés à chaque lancement (jamais pour un jeu seul).
func clearGeneratedSystemDlls() {
	if !packagedPrefix {
		return
	}
	dirs := []string{savePoint + "/drive_c/windows/system32", savePoint + "/drive_c/windows/syswow64"}
	for _, d := range dirs {
		if protonPrefix {
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
	if folderOverlay {
		if !mountOverlay(wsquashfsFile) {
			return false
		}
		mountMode = "overlay"
		overlayKind = "fuse"
		mounted.Store(true)
		uiKVLine("Mode", "overlay (dossier .pc + fuse-overlayfs, dossier non modifié)")
		return true
	}
	if _, err := exec.LookPath("squashfuse"); err != nil {
		return false
	}
	if !mountSquashfs(wsquashfsFile, squashfsPoint) {
		return false
	}
	if _, err := exec.LookPath("fuse-overlayfs"); err == nil && mountOverlay("") {
		mountMode = "overlay"
		overlayKind = "fuse"
		mounted.Store(true)
		uiKVLine("Mode", "overlay (squashfuse + fuse-overlayfs)")
		return true
	}
	fuseUmount(squashfsPoint)
	return false
}

// imageSignature : identité de l'image (chemin, taille, date) : valide le
// cache de la copie.
func imageSignature() string {
	st, err := os.Stat(wsquashfsFile)
	if err != nil {
		return wsquashfsFile + ":"
	}
	return fmt.Sprintf("%s:%d:%d", wsquashfsFile, st.Size(), st.ModTime().Unix())
}

// extractImage : mode copy, extraction dans winePoint (squashfuse, puis
// unsquashfs).
func extractImage() {
	_, sqErr := exec.LookPath("squashfuse")
	if sqErr == nil && mountSquashfs(wsquashfsFile, squashfsPoint) {
		uiKVLine("Mode", "copy (squashfuse)")
		removeCopy()
		_ = os.MkdirAll(winePoint, 0o755)
		if err := runCmd("cp", "-a", squashfsPoint+"/.", winePoint+"/"); err != nil {
			die("échec de la copie depuis squashfuse")
		}
		fuseUmount(squashfsPoint)
	} else if _, err := exec.LookPath("unsquashfs"); err == nil {
		uiKVLine("Mode", "copy (unsquashfs)")
		removeCopy()
		if err := runCmd("unsquashfs", "-d", winePoint, wsquashfsFile); err != nil {
			die("échec de l'extraction unsquashfs")
		}
	} else {
		die("FUSE bloqué et unsquashfs absent",
			"  → Exposer /dev/fuse dans le conteneur : --device /dev/fuse",
			"  → Ou installer squashfs-tools : apt-get install squashfs-tools")
	}
	restoreCopyUsers()
	_ = os.WriteFile(winePoint+"/.wsquashfs_cache", []byte(imageSignature()), 0o644)
	chownToTarget(winePoint)
}

// copyUsersStash : drive_c/users d'une copie de travail (mode copy), mis de
// côté pendant que la copie est supprimée ou refaite. Hors de wine/ : ce
// dossier-là est lu comme une liste de jeux.
func copyUsersStash(name string) string {
	return filepath.Join(cacheDir, "copy-users", name)
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
	stash := copyUsersStash(gameName)
	if !isDirPath(stash) {
		return
	}
	users := winePoint + "/drive_c/users"
	if os.MkdirAll(filepath.Dir(users), 0o755) != nil || os.RemoveAll(users) != nil || os.Rename(stash, users) != nil {
		warn("sauvegardes de la copie non remises en place, gardées dans " + stash)
		return
	}
	uiKVLine("Sauvegardes", "drive_c/users repris de la copie précédente")
}

// removeCopy : supprime l'ancienne copie de travail (drive_c/users mis de
// côté d'abord, remis par restoreCopyUsers après l'extraction). Jamais à travers un
// montage resté en place (démontage raté) : la suppression atteindrait
// l'image ou, en overlay, les sauvegardes.
func removeCopy() {
	if isMountPoint(winePoint) {
		fuseUmount(winePoint)
		if isMountPoint(winePoint) {
			die(winePoint+" est encore monté (démontage impossible) : copie annulée",
				"  → "+os.Args[0]+" --umount \""+wsquashfsFile+"\"")
		}
	}
	if !stashCopyUsers(winePoint, gameName) {
		die("sauvegardes de la copie de travail impossibles à mettre de côté : copie gardée",
			"  ("+winePoint+"/drive_c/users → "+copyUsersStash(gameName)+")")
	}
	_ = os.RemoveAll(winePoint)
}

// useCopyMode : copie de travail en cache, réextraite si l'image a changé.
func useCopyMode() {
	cacheInfo := winePoint + "/.wsquashfs_cache"
	if data, err := os.ReadFile(cacheInfo); err == nil && string(data) == imageSignature() {
		if st, err := os.Stat(winePoint); err == nil && st.IsDir() {
			uiKVLine("Mode", "copy (cache valide, extraction ignorée)")
			mountMode = "copy"
			mounted.Store(true)
			return
		}
	}
	extractImage()
	mountMode = "copy"
	mounted.Store(true)
}

// mountImage : préparation et montage du jeu (overlay, sinon copie).
func mountImage() {
	if gameInUse(gameName) {
		die(gameName + " est déjà lancé")
	}
	unmountAllStale()
	prepareMountDirs()
	detectImagePrefix()
	clearGeneratedSystemDlls()
	runInKernelOverlay()
	if folderOverlay {
		// Jamais de copie : le dossier entier serait recopié.
		if !tryOverlayMountGated() {
			die("montage fuse-overlayfs impossible pour "+wsquashfsFile,
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
	uiSection("Montage")
	pairs := []kv{{"Monté dans", winePoint}}
	if mountMode == "overlay" {
		kind := "le .wsquashfs"
		if folderOverlay {
			kind = "le dossier"
		}
		pairs = append(pairs, kv{"Modifications", "enregistrées dans " + savePoint + " (" + kind + " n'est jamais modifié)"})
	}
	pairs = append(pairs, kv{"Démontage", ""})
	uiKV(2, pairs...)
	uiCode(os.Args[0] + " --umount \"" + wsquashfsFile + "\"")
	uiNote("(tout lancement d'un autre jeu démonte aussi les montages inutilisés)")
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
	raw := regexEscape(cacheDir)
	real := cacheDir
	if r, err := filepath.EvalSymlinks(cacheDir); err == nil {
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
	return gameRunning(name) || isMountPoint(filepath.Join(cacheDir, "wine", name))
}

func lockPath(name string) string {
	return filepath.Join(cacheDir, "locks", name+".lock")
}

// acquireGameLock : verrou du jeu (flock). Faux si déjà tenu.
func acquireGameLock() bool {
	file := lockPath(gameName)
	_ = os.MkdirAll(filepath.Join(cacheDir, "locks"), 0o755)
	chownToTarget(filepath.Join(cacheDir, "locks"))
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
	lockFile = f
	lockedGame = gameName
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
	_ = os.MkdirAll(filepath.Join(cacheDir, "locks"), 0o755)
	chownToTarget(filepath.Join(cacheDir, "locks"))
	f, err := os.OpenFile(lockPath(name), os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		return nil, true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	// Lancé entre le contrôle et la prise du verrou.
	if gameInUse(name) || isMountPoint(filepath.Join(cacheDir, "wine", name)) {
		f.Close()
		return nil, false
	}
	return f, true
}

// gameLocked : un AUTRE lancement tient le verrou de ce jeu.
func gameLocked(name string) bool {
	if name == lockedGame {
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
	realCache := cacheDir
	if r, err := filepath.EvalSymlinks(cacheDir); err == nil {
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
				uiStatusLine(stOK, "Arrêté et démonté : "+name)
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		warn(fmt.Sprintf("%s toujours en cours après 15 s (lanceur %d), non démonté", name, pid))
		exitNow(1)
	}
	prefix := filepath.Join(cacheDir, "wine", name)
	if st, err := os.Stat(prefix); err != nil || !st.IsDir() {
		return
	}
	runWineserver(prefix, "-k")
	umountRetry(prefix)
	umountRetry(filepath.Join(cacheDir, "mnt", name))
	uiStatusLine(stOK, "Démonté : "+name)
}
