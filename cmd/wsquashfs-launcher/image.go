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
		die("fichier .wsquashfs ou dossier de jeu requis",
			"Usage : "+os.Args[0]+" <fichier.wsquashfs | dossier du jeu>  |  "+os.Args[0]+" --help")
	}
	abs, err := filepath.Abs(gameArg)
	if err != nil {
		die("chemin introuvable : " + gameArg)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	wsquashfsFile = abs
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
	fmt.Println("Mode          : dossier (jeu installé, sans FUSE)")
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
	if execDiscard("unsquashfs", "-cat", wsquashfsFile, "system.reg") == nil {
		packagedPrefix = true
	}
	if execDiscard("unsquashfs", "-cat", wsquashfsFile, "config_info") == nil {
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
		mounted = true
		fmt.Println("Mode          : overlay (dossier .pc + fuse-overlayfs, dossier non modifié)")
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
		mounted = true
		fmt.Println("Mode          : overlay (squashfuse + fuse-overlayfs)")
		return true
	}
	doUmount(squashfsPoint)
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
		fmt.Println("Mode          : copy (squashfuse)")
		_ = os.RemoveAll(winePoint)
		_ = os.MkdirAll(winePoint, 0o755)
		if err := runCmd("cp", "-a", squashfsPoint+"/.", winePoint+"/"); err != nil {
			die("échec de la copie depuis squashfuse")
		}
		doUmount(squashfsPoint)
	} else if _, err := exec.LookPath("unsquashfs"); err == nil {
		fmt.Println("Mode          : copy (unsquashfs)")
		_ = os.RemoveAll(winePoint)
		if err := runCmd("unsquashfs", "-d", winePoint, wsquashfsFile); err != nil {
			die("échec de l'extraction unsquashfs")
		}
	} else {
		die("FUSE bloqué et unsquashfs absent",
			"  → Exposer /dev/fuse dans le conteneur : --device /dev/fuse",
			"  → Ou installer squashfs-tools : apt-get install squashfs-tools")
	}
	_ = os.WriteFile(winePoint+"/.wsquashfs_cache", []byte(imageSignature()), 0o644)
	chownToTarget(winePoint)
}

// useCopyMode : copie de travail en cache, réextraite si l'image a changé.
func useCopyMode() {
	cacheInfo := winePoint + "/.wsquashfs_cache"
	if data, err := os.ReadFile(cacheInfo); err == nil && string(data) == imageSignature() {
		if st, err := os.Stat(winePoint); err == nil && st.IsDir() {
			fmt.Println("Mode          : copy (cache valide, extraction ignorée)")
			mountMode = "copy"
			mounted = true
			return
		}
	}
	extractImage()
	mountMode = "copy"
	mounted = true
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
		if !tryOverlayMount() {
			die("montage fuse-overlayfs impossible pour "+wsquashfsFile,
				"  → renommer le dossier en .wine pour le lancer sur place (il sera modifié)")
		}
		return
	}
	if !tryOverlayMount() {
		useCopyMode()
	}
}

// reportMountAndExit : --mount laisse le jeu monté pour une modification à la
// main.
func reportMountAndExit() {
	mounted = false
	fmt.Println("-----------------------------------------")
	fmt.Println("Monté dans    : " + winePoint)
	if mountMode == "overlay" {
		kind := "le .wsquashfs"
		if folderOverlay {
			kind = "le dossier"
		}
		fmt.Println("Modifications : enregistrées dans " + savePoint + " (" + kind + " n'est jamais modifié)")
	}
	fmt.Println("Démontage     : " + os.Args[0] + " --umount \"" + wsquashfsFile + "\"")
	fmt.Println("  (tout lancement d'un autre jeu démonte aussi les montages inutilisés)")
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
				doUmount(m)
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
				fmt.Println("✓ Arrêté et démonté : " + name)
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
	fmt.Println("✓ Démonté : " + name)
}
