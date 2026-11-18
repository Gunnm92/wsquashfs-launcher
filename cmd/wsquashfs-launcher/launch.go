package main

// Lancement d'un jeu (script bash, sections 9.4 à 9.10) : autorun.cmd,
// exécutable, configuration de Wine, prefix, commande du jeu, PRE= et ATTACH=,
// et point d'entrée du lancement. Les manettes sont dans controllers.go.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── 9.4 autorun.cmd ────────────────────────────────────────────────────

// readAutorun : lit l'autorun.cmd du prefix et fixe les réglages du jeu.
func readAutorun() {
	data, err := os.ReadFile(launch.winePoint + "/autorun.cmd")
	// Dossier sans autorun.cmd : CMD vient des couches (ensureFolderCmd).
	if err != nil && launch.action != "exec" && !launch.folderOverlay {
		die("autorun.cmd introuvable dans le wsquashfs")
	}
	loadLayers(string(data))

	launch.dir = unquote(autorunVar("DIR", ""))
	launch.cmd = autorunVar("CMD", "")
	launch.wineVersion = autorunVar("WINE", "")
	launch.protonVersion = autorunVar("PROTON", "")
	launch.runnerKey = autorunVar("RUNNER", "")
	launch.wineArch = autorunVar("ARCH", "")

	launch.dxvk = autorunVar("DXVK", "")
	launch.dxvkWanted = launch.dxvk
	if launch.dxvk == "1" && findDxvkDir() == "" {
		warn("DXVK introuvable (/usr/share/dxvk), rendu via wined3d")
		launch.dxvk = "0"
	}
	launch.vkd3d = autorunVar("VKD3D", "")
	if launch.vkd3d == "1" && findVkd3dDir() == "" {
		warn("VKD3D-Proton introuvable (/usr/share/vkd3d-proton), Direct3D 12 indisponible")
		launch.vkd3d = "0"
	}
	launch.d7vk = autorunVar("D7VK", "")
	if launch.d7vk == "1" && findD7vk() == "" {
		launch.d7vk = "0"
	}

	launch.savedirKey = unquote(autorunVar("SAVEDIR", ""))
	launch.savefilesKey = unquote(autorunVar("SAVEFILES", ""))
	launch.esync = autorunVar("ESYNC", "")
	launch.fsync = autorunVar("FSYNC", "")
	launch.nvapi = autorunVar("NVAPI", "")
	launch.vkreflex = autorunVar("VKREFLEX", "")
	launch.fsr = autorunVar("FSR", "")
	launch.fsr4 = autorunVar("FSR4", "")
	launch.wineLang = autorunVar("LANG", "")
	launch.wineEnv = autorunVar("ENV", "")
	launch.preCmd = autorunVar("PRE", "")
	launch.attachCmd = autorunVar("ATTACH", "")
	launch.attachProcess = unquote(autorunVar("ATTACH_PROCESS", ""))
	launch.virtualDesktop = autorunVar("VIRTUAL_DESKTOP", "")

	if launch.cmd == "" && launch.action != "exec" {
		die("CMD non défini dans autorun.cmd")
	}
}

// ── 9.5 Exécutable ─────────────────────────────────────────────────────

var cmdQuotedRe = regexp.MustCompile(`^"([^"]+)"(.*)$`)

// resolveExecutable : exécutable du jeu (CMD) ou programme de --exec, résolus
// sans tenir compte de la casse.
func resolveExecutable() {
	launch.dir = strings.ReplaceAll(launch.dir, `\`, "/")
	if launch.dir != "" {
		if resolved, ok := resolveCI(launch.winePoint, launch.dir); ok {
			launch.dir = strings.TrimPrefix(resolved, launch.winePoint+"/")
		}
	}
	if launch.action == "exec" {
		prog := strings.ReplaceAll(launch.execCmd[0], `\`, "/")
		launch.execArgsArr = launch.execCmd[1:]
		launch.execArgs = strings.Join(launch.execArgsArr, " ")
		if p, ok := resolveCI(launch.winePoint, prog); ok && isFile(p) {
			launch.execPath = p
			launch.dir = filepath.Dir(strings.TrimPrefix(p, launch.winePoint+"/"))
			if launch.dir == "." {
				launch.dir = ""
			}
		} else if p, ok := resolveCI(launch.winePoint, launch.dir+"/"+prog); launch.dir != "" && ok && isFile(p) {
			launch.execPath = p
			launch.dir = filepath.Dir(strings.TrimPrefix(p, launch.winePoint+"/"))
		} else {
			launch.execPath = launch.execCmd[0]
			launch.dir = ""
		}
	} else {
		var execName string
		if m := cmdQuotedRe.FindStringSubmatch(launch.cmd); m != nil {
			execName = m[1]
			launch.execArgs = strings.TrimPrefix(m[2], " ")
		} else {
			fields := strings.SplitN(launch.cmd, " ", 2)
			execName = fields[0]
			launch.execArgs = ""
			if len(fields) == 2 {
				launch.execArgs = fields[1]
			}
		}
		launch.execArgsArr = splitCommandLine(launch.execArgs)
		rel := execName
		if launch.dir != "" {
			rel = launch.dir + "/" + execName
		}
		if p, ok := resolveCI(launch.winePoint, rel); ok {
			launch.execPath = p
		} else {
			launch.execPath = launch.winePoint + "/" + rel
		}
		if !isFile(launch.execPath) {
			die("exécutable introuvable : "+launch.execPath,
				"  CMD : "+launch.cmd+"  |  DIR : "+orEmpty(launch.dir, "<vide>"))
		}
	}
	launch.ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(launch.execPath), "."))
}

// orEmpty : v, ou repli si vide.
func orEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// ── 9.7 Configuration de Wine ──────────────────────────────────────────

// ensureWin32Runner : un prefix win32 demande un Wine « classique » (i386-unix).
func ensureWin32Runner() {
	if launch.wineArch != "win32" || wineSupportsWin32(launch.wineBinary) {
		return
	}
	if launch.wineOverride != "" {
		die("prefix win32, mais le runner choisi (--wine " + launch.wineOverride + ") est WoW64 seul")
	}
	alt := findWin32Wine()
	if alt == "" {
		die("prefix win32, mais "+runnerName(launch.wineBinary)+" est un Wine WoW64 seul",
			"  (pas de lib/wine/i386-unix) qui ne peut pas lancer de prefix 32 bits.",
			"  → Installer un Wine classique (ex. Kron4ek wine-*-amd64, non wow64)",
			"    dans /opt ou ~/.local/share/wsquashfs/wine")
	}
	launch.wineBinary = alt
}

// configureDllOverrides : overrides DLL de Wine direct, ou équivalents Proton.
func configureDllOverrides() {
	if launch.umuRun != "" {
		// Proton pose lui-même DXVK/VKD3D/NVAPI dans le prefix.
		if launch.dxvkWanted == "0" {
			os.Setenv("PROTON_USE_WINED3D", "1")
		}
		launch.dxvk, launch.vkd3d, launch.d7vk = "0", "0", "0"
		// Proton COPIE ~560 DLL par prefix sinon : liens (PROTON_DLL_COPY vide).
		os.Setenv("PROTON_DLL_COPY", os.Getenv("PROTON_DLL_COPY"))
	} else {
		if launch.d7vk == "1" {
			addDllOverride("ddraw=n,b")
		}
		if launch.dxvk == "1" {
			addDllOverride("d3d8,d3d9,d3d10core,d3d11,dxgi=n,b")
		} else {
			addDllOverride("d3d8,d3d9,d3d10core,d3d11,dxgi=b")
		}
		if launch.vkd3d == "1" {
			addDllOverride("d3d12,d3d12core=n,b")
		}
		if launch.nvapiOn == "1" {
			os.Setenv("DXVK_ENABLE_NVAPI", "1")
			addDllOverride("nvapi,nvapi64=n")
		} else {
			os.Setenv("DXVK_ENABLE_NVAPI", "0")
			addDllOverride("nvapi64,nvapi=")
		}
	}
	// Borne arcade : Wine n'ouvre rien côté Linux.
	addDllOverride("winemenubuilder.exe=d;winebrowser.exe=d")
}

// wineEnvKeyRe : début d'un couple NOM=valeur de l'environnement Wine.
var wineEnvKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// applyWineEnv : ENV= de l'autorun, NOM=valeur séparés par des espaces.
func applyWineEnv(env string) {
	for _, pair := range strings.Fields(env) {
		if wineEnvKeyRe.MatchString(pair) {
			k, v, _ := strings.Cut(pair, "=")
			os.Setenv(k, v)
		}
	}
}

// configureWine : architecture, GPU, environnement Wine du jeu.
func configureWine() {
	if launch.wineArch == "" {
		launch.wineArch = detectPrefixArch()
	}
	ensureWin32Runner()
	detectGpus()
	resolveNvapi()
	resolveVkreflex()
	resolveFsr4()
	configureGpuEnv()

	// Outils Wine (regedit, reg) : par umu pour un prefix Proton.
	launch.wineTool = []string{launch.wineBinary}
	if launch.umuRun != "" {
		launch.wineTool = []string{launch.umuRun}
	}

	os.Setenv("WINEPREFIX", launch.winePoint)
	os.Setenv("WINEARCH", launch.wineArch)

	// Nom d'utilisateur Wine adapté au prefix (steamuser, Batocera).
	prefixUser := detectWineUsername()
	effective := os.Getenv("USER")
	if dropsToTargetUser() {
		effective = conf.targetUser
	}
	if prefixUser != "" && prefixUser != effective {
		os.Setenv("WINEUSERNAME", prefixUser)
	}

	if launch.esync == "1" {
		os.Setenv("WINEESYNC", "1")
	}
	if launch.fsync == "1" {
		os.Setenv("WINEFSYNC", "1")
	}
	configureDllOverrides()

	// Batocera exporte WINEDEBUG=-all ; on garde les err: pour le diagnostic.
	if os.Getenv("WINEDEBUG") == "" {
		os.Setenv("WINEDEBUG", "fixme-all")
	}
	if launch.wineLang != "" {
		os.Setenv("LC_ALL", launch.wineLang)
	}
	if launch.wineEnv != "" {
		applyWineEnv(launch.wineEnv)
	}
}

// ── 9.8 Préparation du prefix ──────────────────────────────────────────

// initPrefixIfNeeded : crée le prefix d'un jeu seul, ou répare un prefix sans
// system32.
func initPrefixIfNeeded() {
	if launch.umuRun != "" {
		if !isFile(launch.winePoint + "/system.reg") {
			uiStatusLine(stInfo, "Initialisation du préfixe par Proton au premier lancement...")
		}
		return
	}
	if isFile(launch.winePoint + "/system.reg") {
		if st, err := os.Stat(launch.winePoint + "/drive_c"); err == nil && st.IsDir() &&
			(launch.packagedPrefix || isFile(launch.winePoint+"/drive_c/windows/system32/explorer.exe")) {
			uiStatusLine(stOK, "Préfixe Wine détecté (structure Batocera)")
			return
		}
	}
	uiStatusLine(stInfo, "Initialisation du préfixe Wine ("+launch.wineArch+")...")
	// wineboot -u ne réinstalle rien tant que .update-timestamp correspond.
	_ = os.Remove(launch.winePoint + "/.update-timestamp")
	_ = runWineboot(os.Getenv("WINEDEBUG"))
	_ = runWineserver(launch.winePoint, "-w")
	// Overlay : jamais de chown sur WINEPOINT (le montage), l'upperdir suffit.
	if launch.mountMode == "overlay" {
		chownToTarget(launch.savePoint)
	} else {
		chownToTarget(launch.winePoint)
	}
}

// installPrefixDlls : DLL du runner, DXVK, VKD3D, d7vk et NVAPI (sans umu).
func installPrefixDlls() {
	if launch.umuRun != "" {
		return
	}
	if launch.mountMode == "overlay" && launch.overlayKind == "fuse" {
		linkRuntimeDlls(launch.savePoint)
		chownToTarget(launch.savePoint)
		reloadOverlay("après sync DLLs")
		return
	}
	linkRuntimeDlls(launch.winePoint)
	chownToTarget(launch.winePoint)
}

// importRegistryFiles : regs/*.reg importés à chaque lancement.
func importRegistryFiles() {
	regs := launch.winePoint + "/regs"
	if st, err := os.Stat(regs); err != nil || !st.IsDir() {
		return
	}
	uiStatusLine(stInfo, "Import des fichiers de registre...")
	files, _ := filepath.Glob(regs + "/*")
	for _, f := range files {
		if !strings.EqualFold(filepath.Ext(f), ".reg") {
			continue
		}
		uiNote(filepath.Base(f))
		_ = runWineTool("regedit", "//?/unix"+f)
	}
}

// preparePrefix : prefix prêt à lancer.
func preparePrefix() {
	initPrefixIfNeeded()
	if launch.umuRun == "" {
		updatePrefixIfNeeded()
	}
	fixUserdataLinks()
	installPrefixDlls()
	if launch.umuRun == "" {
		writeNvidiaRegistry()
	}
	importRegistryFiles()
}

// ── 9.9 / 9.10 Récapitulatif, commande et exécution ────────────────────

// printSummary : récapitulatif du lancement.
func printSummary() {
	pairs := []kv{{"Exécutable", launch.execPath}, {"Arguments", orEmpty(launch.execArgs, "<aucun>")}}
	if launch.umuRun != "" {
		if launch.protonDir+"/files/bin/wine" == launch.imageRunner {
			pairs = append(pairs, kv{"Proton", filepath.Base(launch.protonDir) + " via umu (" + launch.protonDir + ") — joint à l'image"})
		} else {
			pairs = append(pairs, kv{"Proton", filepath.Base(launch.protonDir) + " via umu (" + launch.protonDir + ")"})
		}
		if v := runnerVersion(launch.protonDir + "/files/bin/wine"); v != "" {
			pairs = append(pairs, kv{"Version Proton", v})
		}
	} else {
		if launch.imageRunner != "" && launch.wineBinary == launch.imageRunner {
			pairs = append(pairs, kv{"Wine", launch.wineBinary + " (" + launch.wineArch + ") — joint à l'image"})
		} else {
			pairs = append(pairs, kv{"Wine", launch.wineBinary + " (" + launch.wineArch + ")"})
		}
		if v := runnerVersion(launch.wineBinary); v != "" {
			pairs = append(pairs, kv{"Version Wine", v})
		}
	}
	pairs = append(pairs, kv{"Préfixe", os.Getenv("WINEPREFIX")})
	if u := os.Getenv("WINEUSERNAME"); u != "" {
		pairs = append(pairs, kv{"Utilisateur", u + " (adapté au prefix)"})
	}
	if conf.runAsRoot {
		if conf.targetUser != "" {
			pairs = append(pairs, kv{"Privilège", "montage en root, Wine relancé en " + conf.targetUser})
		} else {
			warn("lancé en root sans SUDO_USER — l'affichage risque d'échouer (pas de cookie X11)")
		}
	}
	if launch.mountMode == "overlay" {
		pairs = append(pairs, kv{"Sauvegardes", launch.savePoint})
	}
	pairs = append(pairs, kv{"Manettes", launch.hidrawMsg})
	if launch.dxvk == "1" {
		pairs = append(pairs, kv{"DXVK", "activé"})
	}
	if launch.vkd3d == "1" {
		pairs = append(pairs, kv{"VKD3D", "activé"})
	}
	if launch.d7vk == "1" {
		pairs = append(pairs, kv{"D7VK", "activé (DirectDraw → Vulkan)"})
	}
	if launch.esync == "1" {
		pairs = append(pairs, kv{"ESYNC", "activé"})
	}
	if launch.fsync == "1" {
		pairs = append(pairs, kv{"FSYNC", "activé"})
	}
	if launch.wineLang != "" {
		pairs = append(pairs, kv{"Langue", launch.wineLang})
	}
	if launch.virtualDesktop != "" {
		pairs = append(pairs, kv{"Bureau virtuel", launch.virtualDesktop})
	}
	uiSection("Lancement")
	uiKV(2, pairs...)
}

// desktopNameRe : caractères retirés du nom de bureau créé pour le jeu.
var desktopNameRe = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// virtualDesktopRe : taille WxH d'un bureau virtuel.
var virtualDesktopRe = regexp.MustCompile(`^([0-9]+)x([0-9]+)$`)

// buildRunCommand : ligne de commande du jeu (wine [explorer /desktop] [cmd /c]
// exe args), umu-run à la place de wine, gamescope pour --headless.
func buildRunCommand() {
	launch.runCmdArgs = []string{launch.wineBinary}
	if launch.virtualDesktop != "" {
		desktop := desktopNameRe.ReplaceAllString(strings.ReplaceAll(launch.gameName, " ", "_"), "")
		launch.runCmdArgs = append(launch.runCmdArgs, "explorer", "/desktop="+desktop+","+launch.virtualDesktop)
	}
	if launch.ext == "bat" {
		launch.runCmdArgs = append(launch.runCmdArgs, "cmd", "/c")
	}
	launch.runCmdArgs = append(launch.runCmdArgs, launch.execPath)
	launch.runCmdArgs = append(launch.runCmdArgs, launch.execArgsArr...)
	if launch.umuRun != "" {
		launch.runCmdArgs[0] = launch.umuRun
	}
	if launch.headless {
		w, h := "1920", "1080"
		if m := virtualDesktopRe.FindStringSubmatch(launch.virtualDesktop); m != nil {
			w, h = m[1], m[2]
		}
		launch.runCmdArgs = append([]string{"gamescope", "--backend", "headless", "-w", w, "-h", h, "-W", w, "-H", h, "--"}, launch.runCmdArgs...)
	}
}

// gameEnvNames : variables transmises au jeu (run_as_target_user et play_game).
var gameEnvNames = []string{
	"DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "WINEPREFIX", "WINEARCH", "WINEDLLOVERRIDES",
	"WINEESYNC", "WINEFSYNC", "WINEUSERNAME", "LC_ALL", "WINEDEBUG", "GAMEID", "PROTONPATH",
	"SDL_GAMECONTROLLERCONFIG", "SDL_JOYSTICK_DISABLE_UDEV", "PROTON_SONY_HIDRAW_XINPUT",
	"MANGOHUD", "MANGOHUD_CONFIG", "MANGOHUD_CONFIGFILE",
}

// gameEnvOptional : variables transmises seulement si définies.
var gameEnvOptional = []string{
	"DXVK_ENABLE_NVAPI", "DXVK_NVAPI_VKREFLEX", "DISABLE_DXVK_NVAPI_VKREFLEX", "VK_ADD_IMPLICIT_LAYER_PATH",
	"DXVK_STATE_CACHE_PATH", "DXVK_LOG_LEVEL", "VKD3D_SHADER_CACHE_PATH", "VKD3D_DEBUG", "WINE_MONO_OVERRIDES",
	"__GL_SHADER_DISK_CACHE_PATH", "__GL_SHADER_DISK_CACHE_SIZE", "__GL_SHADER_DISK_CACHE_SKIP_CLEANUP",
	"PROTON_USE_WINED3D", "PYTHONDONTWRITEBYTECODE",
}

// runAsTargetUser : commande lancée en TARGET_USER avec l'environnement du jeu
// (env explicite plutôt que sudo --preserve-env).
func runAsTargetUser(args ...string) *exec.Cmd {
	env := []string{}
	for _, n := range gameEnvNames {
		env = append(env, n+"="+os.Getenv(n))
	}
	env = append(env, "PROTON_DLL_COPY="+os.Getenv("PROTON_DLL_COPY"))
	for _, n := range gameEnvOptional {
		if v := os.Getenv(n); v != "" {
			env = append(env, n+"="+v)
		}
	}
	return exec.Command("sudo", append([]string{"-u", conf.targetUser, "env"}, append(env, args...)...)...)
}

// playGame : le jeu lui-même, avec l'utilisateur de la partie.
func playGame() int {
	var cmd *exec.Cmd
	if dropsToTargetUser() {
		cmd = runAsTargetUser(launch.runCmdArgs...)
	} else {
		cmd = asWineUser(launch.runCmdArgs[0], launch.runCmdArgs[1:]...)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if launch.lockFile != nil {
		cmd.ExtraFiles = []*os.File{launch.lockFile}
	}
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}

// asGameUser : commande avec l'utilisateur et l'environnement du jeu.
func asGameUser(args ...string) *exec.Cmd {
	if dropsToTargetUser() {
		return runAsTargetUser(args...)
	}
	return asWineUser(args[0], args[1:]...)
}

// runPreCommand : PRE= (autorun.cmd), dans le dossier du jeu. Un échec arrête
// le lancement.
func runPreCommand() {
	if launch.preCmd == "" {
		return
	}
	// Dossier courant : celui du jeu (runGame vient de s'y placer).
	cmd := asGameUser("bash", "-c", launch.preCmd)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if cmd.Run() != nil {
		die("PRE a échoué : " + launch.preCmd)
	}
}

// gameProcessPID : PID du processus du jeu (nom du noyau, 15 caractères) dont
// l'environnement a WINEPREFIX de ce jeu.
func gameProcessPID(name string) int {
	prefixes := []string{regexp.QuoteMeta(launch.winePoint)}
	if real, err := filepath.EvalSymlinks(launch.winePoint); err == nil && real != launch.winePoint {
		prefixes = append(prefixes, regexp.QuoteMeta(real))
	}
	re := regexp.MustCompile("^WINEPREFIX=(" + strings.Join(prefixes, "|") + ")(/.*)?$")
	short := name
	if len(short) > 15 {
		short = short[:15]
	}
	procs, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, c := range procs {
		comm, err := os.ReadFile(c)
		if err != nil || strings.TrimSpace(string(comm)) != short {
			continue
		}
		pid, _ := strconv.Atoi(filepath.Base(filepath.Dir(c)))
		env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err != nil {
			continue
		}
		for _, kv := range strings.Split(string(env), "\x00") {
			if re.MatchString(kv) {
				return pid
			}
		}
	}
	return 0
}

// startAttachCommand : ATTACH= en tâche de fond, pendant toute la partie. Attend
// le processus du jeu, puis lance la commande avec GAME_PID.
func startAttachCommand() {
	if launch.attachCmd == "" {
		return
	}
	if launch.attachProcess == "" {
		launch.attachProcess = filepath.Base(launch.execPath)
	}
	attachMarker = fmt.Sprintf("WSQUASHFS_ATTACH=%d", os.Getpid())
	attachMu.Lock()
	attachStop = make(chan struct{})
	attachDone = make(chan struct{})
	// Canal local : la variable globale est remise à nil à l'arrêt.
	stop, done := attachStop, attachDone
	attachMu.Unlock()
	go func() {
		defer close(done)
		var pid int
		for {
			select {
			case <-stop:
				// Partie finie avant le processus du jeu (trap USR1 du script).
				if pid == 0 {
					warn("ATTACH : processus " + launch.attachProcess + " introuvable, commande non lancée")
				}
				return
			default:
			}
			if pid = gameProcessPID(launch.attachProcess); pid != 0 {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		cmd := asGameUser("env", attachMarker, "GAME_PID="+strconv.Itoa(pid), "bash", "-c", launch.attachCmd)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}()
}

var (
	attachMarker string
	attachMu     sync.Mutex    // stopAttachCommand : fin de partie et cleanup concurrents
	attachStop   chan struct{} // fin de la partie : abandonne la recherche
	attachDone   chan struct{} // la goroutine ATTACH est terminée
)

// stopAttachCommand : arrête ATTACH et ce qu'elle a lancé (marqueur
// WSQUASHFS_ATTACH). Une commande lancée a 5 s pour finir seule.
func stopAttachCommand() {
	// Un seul arrêt à la fois : le second appel attend la fin du premier puis
	// trouve attachStop à nil (pas de double close).
	attachMu.Lock()
	defer attachMu.Unlock()
	if attachStop == nil {
		return
	}
	// Recherche du processus abandonnée (SIGUSR1 du script) ; une commande déjà
	// lancée a 5 s pour finir seule, puis tout ce qu'elle a lancé est tué.
	close(attachStop)
	attachStop = nil
	select {
	case <-attachDone:
	case <-time.After(5 * time.Second):
	}
	killByEnv(attachMarker)
	select {
	case <-attachDone:
	case <-time.After(time.Second):
	}
}

// runGame : le jeu, puis attente de tous les processus du prefix.
func runGame() {
	os.Chdir(launch.winePoint + ifNotEmpty(launch.dir))
	buildRunCommand()
	if launch.action != "exec" {
		runPreCommand()
		startAttachCommand()
	}
	uiRule("sortie du jeu")
	code := playGame()
	if signalExiting.Load() {
		select {} // sortie sur signal en cours : cleanup puis os.Exit
	}
	_ = runWineserver(launch.winePoint, "-w")
	stopAttachCommand()
	if launch.action == "run" { // lancement normal seulement (pas --exec)
		autoSyncAfterGame(launch.gameName)
	}
	uiRule("")
	uiVerdict(code == 0, fmt.Sprintf("Jeu terminé (code : %d)", code))
	exitNow(code)
}

// ifNotEmpty : "/"+v si v est non vide, "" sinon.
func ifNotEmpty(v string) string {
	if v == "" {
		return ""
	}
	return "/" + v
}

// launchGame : enchaîne les étapes du lancement (launch_game).
func launchGame() {
	resolveGamePath()
	checkLauncherDeps()
	initGamePaths()
	if launch.nsStage != "" {
		// Relancé dans le namespace : verrou hérité, dossiers préparés par l'hôte.
		launch.lockedGame = launch.gameName
		useKernelOverlay()
	} else {
		printLauncherHeader()
		if !acquireGameLock() {
			die(launch.gameName + " est déjà lancé")
		}
		if launch.dirMode {
			useInstalledDir()
		} else {
			mountImage()
		}
	}
	if launch.action == "mount" {
		reportMountAndExit()
	}
	ensureFolderCmd()
	readAutorun()
	resolveExecutable()
	selectRunner()
	configureWine()
	preparePrefix()
	if launch.action == "prepare" {
		applySavedir(launch.savedirKey, launch.savefilesKey)
		uiVerdict(true, "Prefix préparé : "+launch.gameName+" (jeu non lancé)")
		exitNow(0)
	}
	configureControllers()
	applySavedir(launch.savedirKey, launch.savefilesKey)
	printSummary()
	runGame()
}

// printLauncherHeader : en-tête du lancement.
func printLauncherHeader() {
	sub := launch.gameName
	if v := gameVersion(launch.wsquashfsFile); v != "" {
		sub += " · version " + v
	}
	uiTitle("WSquashFS Launcher", sub)
	uiKVLine("Environnement", detectEnv())
	uiKVLine("Fichier", launch.wsquashfsFile)
	if f := machineLayerFile(); isFile(f) {
		uiKVLine("Réglages machine", f+" ("+overrideKeys(f)+")")
	}
	if f := filepath.Dir(strings.TrimSuffix(launch.wsquashfsFile, "/")) + "/_defaults.autorun"; isFile(f) {
		uiKVLine("Réglages dossier", f+" ("+overrideKeys(f)+")")
	}
	if launch.autorunOverride != "" {
		uiKVLine("Surcharge", filepath.Base(launch.autorunOverride)+" ("+overrideKeys(launch.autorunOverride)+")")
	}
	if len(launch.setArgs) > 0 {
		uiKVLine("Options --set", strings.Join(launch.setArgs, " "))
	}
	if launch.action != "run" {
		name := ""
		if len(launch.execCmd) > 0 {
			name = " (" + launch.execCmd[0] + ")"
		}
		uiKVLine("Action", launch.action+name)
	}
}
