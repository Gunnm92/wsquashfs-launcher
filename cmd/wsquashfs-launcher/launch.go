package main

// Lancement d'un jeu (script bash, sections 7 et 9.4 à 9.10) : manettes,
// autorun.cmd, exécutable, configuration de Wine, prefix, commande du jeu,
// PRE= et ATTACH=, et point d'entrée du lancement.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ── 7. Manettes ────────────────────────────────────────────────────────

var reSonyHid = regexp.MustCompile(`(?m)^HID_ID=[0-9A-Fa-f]*:0000054C:`)

// hidrawSysfs : /sys/class/hidraw, surchargeable (tests).
func hidrawSysfs() string {
	return envOr("HIDRAW_SYSFS", "/sys/class/hidraw")
}

// hidrawUsable : manettes Sony lisibles par hidraw. Faux avec la raison dans
// hidrawReason si une manette est illisible ou absente.
func hidrawUsable() bool {
	uevents, _ := filepath.Glob(hidrawSysfs() + "/*/device/uevent")
	found := false
	for _, u := range uevents {
		data, err := os.ReadFile(u)
		if err != nil || !reSonyHid.Match(data) {
			continue
		}
		found = true
		n := filepath.Base(filepath.Dir(filepath.Dir(u)))
		if f, err := os.Open("/dev/" + n); err != nil {
			hidrawReason = "/dev/" + n + " illisible"
			return false
		} else {
			f.Close()
		}
	}
	if !found {
		hidrawReason = "aucune manette Sony"
		return false
	}
	return true
}

// findDualsenseBridge : dualsense-usb-bridge dans le PATH, sinon à côté du
// lanceur (dépôt).
func findDualsenseBridge() string {
	if p, err := exec.LookPath("dualsense-usb-bridge"); err == nil {
		return p
	}
	p := filepath.Dir(selfPath) + "/tools/dualsense-usb-bridge/dualsense-usb-bridge"
	if isExecutable(p) {
		return p
	}
	return ""
}

// startDualsenseBridge : DualSense Bluetooth présentée en DualSense USB pendant
// le jeu (HIDRAW=1). Faux s'il n'y a pas de DualSense Bluetooth ou pas de pont.
func startDualsenseBridge() bool {
	bt, _ := filepath.Glob(hidrawSysfs() + "/*/device/uevent")
	isBT := false
	for _, u := range bt {
		if data, err := os.ReadFile(u); err == nil && strings.Contains("\n"+string(data), "\nHID_ID=0005:0000054C:00000CE6\n") {
			isBT = true
		}
	}
	if !isBT {
		return false
	}
	bridge := findDualsenseBridge()
	if bridge == "" {
		warn("dualsense-usb-bridge introuvable, DualSense Bluetooth en direct")
		return false
	}
	ready := fmt.Sprintf("%s/dualsense-usb-bridge.%d", envOr("TMPDIR", "/tmp"), os.Getpid())
	cmd := exec.Command(bridge, "--hide", "--parent", strconv.Itoa(os.Getpid()), "--ready-file", ready)
	if err := cmd.Start(); err != nil {
		return false
	}
	dsBridgePID = cmd.Process.Pid
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	for i := 0; i < 30; i++ {
		if _, err := os.Stat(ready); err == nil {
			_ = os.Remove(ready)
			return true
		}
		select {
		case <-exited:
			_ = os.Remove(ready)
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	stopDualsenseBridge()
	_ = os.Remove(ready)
	return false
}

// stopDualsenseBridge : arrête le pont DualSense.
func stopDualsenseBridge() {
	if dsBridgePID == 0 {
		return
	}
	_ = syscall.Kill(dsBridgePID, syscall.SIGTERM)
	dsBridgePID = 0
}

// chooseHidrawMode : HIDRAW=1 (pont USB si DualSense Bluetooth), HIDRAW=bt
// (DualSense Bluetooth en direct), sinon XInput.
func chooseHidrawMode() {
	hidrawDisable = "1"
	hidrawMsg = "XInput, manette Xbox"
	mode := autorunVar("HIDRAW", "0")
	if mode != "1" && mode != "bt" {
		return
	}
	if !hidrawUsable() {
		hidrawMsg = "XInput, manette Xbox (hidraw demandé : " + hidrawReason + ")"
		return
	}
	hidrawDisable = "0"
	hidrawMsg = "DualSense/DualShock en direct (hidraw)"
	if mode == "1" && startDualsenseBridge() {
		hidrawMsg = "DualSense USB (dualsense-usb-bridge, depuis la DualSense Bluetooth)"
	}
}

// applyGeProtonSonyFix : GE-Proton ignore les manettes Sony en SDL ; hidraw
// coupé, sa conversion XInput les présente en XInput (PROTON_SONY_HIDRAW_XINPUT).
func applyGeProtonSonyFix() {
	protonRoot := ""
	if strings.HasSuffix(wineBinary, "/files/bin/wine") {
		protonRoot = strings.TrimSuffix(wineBinary, "/files/bin/wine")
	}
	if hidrawDisable != "1" || protonRoot == "" {
		return
	}
	lib, err := os.ReadFile(protonRoot + "/files/lib/wine/x86_64-unix/winebus.so")
	if err != nil || !strings.Contains(string(lib), "hidraw handles native reports") || !hidrawUsable() {
		return
	}
	hidrawDisable = "0"
	os.Setenv("PROTON_SONY_HIDRAW_XINPUT", "1")
	hidrawMsg = "XInput, manette Xbox (manette Sony par hidraw, convertie par GE-Proton)"
}

// writeHidrawSetting : DisableHidraw inscrit dans le registre s'il diffère.
func writeHidrawSetting() {
	want := `"DisableHidraw"=dword:0000000` + hidrawDisable
	for _, f := range []string{winePoint + "/system.reg", winePoint + "/pfx/system.reg"} {
		if data, err := os.ReadFile(f); err == nil && strings.Contains(string(data), want) {
			return
		}
	}
	_ = runWineTool("reg", "add", `HKLM\System\CurrentControlSet\Services\winebus`,
		"/v", "DisableHidraw", "/t", "REG_DWORD", "/d", hidrawDisable, "/f")
	_ = runWineserver(winePoint, "-k")
	_ = runWineserver(winePoint, "-w")
}

// configureControllers : mode des manettes, correctif GE-Proton, registre.
func configureControllers() {
	chooseHidrawMode()
	applyGeProtonSonyFix()
	writeHidrawSetting()
}

// ── 9.4 autorun.cmd ────────────────────────────────────────────────────

// readAutorun : lit l'autorun.cmd du prefix et fixe les réglages du jeu.
func readAutorun() {
	data, err := os.ReadFile(winePoint + "/autorun.cmd")
	if err != nil && action != "exec" {
		die("autorun.cmd introuvable dans le wsquashfs")
	}
	loadLayers(string(data))

	dir = unquote(autorunVar("DIR", ""))
	cmd = autorunVar("CMD", "")
	wineVersion = autorunVar("WINE", "")
	protonVersion = autorunVar("PROTON", "")
	runnerKey = autorunVar("RUNNER", "")
	wineArch = autorunVar("ARCH", "")

	dxvk = autorunVar("DXVK", "")
	dxvkWanted = dxvk
	if dxvk == "1" && findDxvkDir() == "" {
		warn("DXVK introuvable (/usr/share/dxvk), rendu via wined3d")
		dxvk = "0"
	}
	vkd3d = autorunVar("VKD3D", "")
	if vkd3d == "1" && findVkd3dDir() == "" {
		warn("VKD3D-Proton introuvable (/usr/share/vkd3d-proton), Direct3D 12 indisponible")
		vkd3d = "0"
	}
	d7vk = autorunVar("D7VK", "")
	if d7vk == "1" && findD7vk() == "" {
		d7vk = "0"
	}

	savedirKey = unquote(autorunVar("SAVEDIR", ""))
	savefilesKey = unquote(autorunVar("SAVEFILES", ""))
	esync = autorunVar("ESYNC", "")
	fsync = autorunVar("FSYNC", "")
	nvapi = autorunVar("NVAPI", "")
	vkreflex = autorunVar("VKREFLEX", "")
	fsr = autorunVar("FSR", "")
	fsr4 = autorunVar("FSR4", "")
	wineLang = autorunVar("LANG", "")
	wineEnv = autorunVar("ENV", "")
	preCmd = autorunVar("PRE", "")
	attachCmd = autorunVar("ATTACH", "")
	attachProcess = unquote(autorunVar("ATTACH_PROCESS", ""))
	virtualDesktop = autorunVar("VIRTUAL_DESKTOP", "")

	if cmd == "" && action != "exec" {
		die("CMD non défini dans autorun.cmd")
	}
}

// ── 9.5 Exécutable ─────────────────────────────────────────────────────

var cmdQuotedRe = regexp.MustCompile(`^"([^"]+)"(.*)$`)

// resolveExecutable : exécutable du jeu (CMD) ou programme de --exec, résolus
// sans tenir compte de la casse.
func resolveExecutable() {
	dir = strings.ReplaceAll(dir, `\`, "/")
	if dir != "" {
		if resolved, ok := resolveCI(winePoint, dir); ok {
			dir = strings.TrimPrefix(resolved, winePoint+"/")
		}
	}
	if action == "exec" {
		prog := strings.ReplaceAll(execCmd[0], `\`, "/")
		execArgsArr = execCmd[1:]
		execArgs = strings.Join(execArgsArr, " ")
		if p, ok := resolveCI(winePoint, prog); ok && isFile(p) {
			execPath = p
			dir = filepath.Dir(strings.TrimPrefix(p, winePoint+"/"))
			if dir == "." {
				dir = ""
			}
		} else if p, ok := resolveCI(winePoint, dir+"/"+prog); dir != "" && ok && isFile(p) {
			execPath = p
			dir = filepath.Dir(strings.TrimPrefix(p, winePoint+"/"))
		} else {
			execPath = execCmd[0]
			dir = ""
		}
	} else {
		var execName string
		if m := cmdQuotedRe.FindStringSubmatch(cmd); m != nil {
			execName = m[1]
			execArgs = strings.TrimPrefix(m[2], " ")
		} else {
			fields := strings.SplitN(cmd, " ", 2)
			execName = fields[0]
			execArgs = ""
			if len(fields) == 2 {
				execArgs = fields[1]
			}
		}
		execArgsArr = splitCommandLine(execArgs)
		rel := execName
		if dir != "" {
			rel = dir + "/" + execName
		}
		if p, ok := resolveCI(winePoint, rel); ok {
			execPath = p
		} else {
			execPath = winePoint + "/" + rel
		}
		if !isFile(execPath) {
			die("exécutable introuvable : "+execPath,
				"  CMD : "+cmd+"  |  DIR : "+orEmpty(dir, "<vide>"))
		}
	}
	ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(execPath), "."))
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
	if wineArch != "win32" || wineSupportsWin32(wineBinary) {
		return
	}
	if wineOverride != "" {
		die("prefix win32, mais le runner choisi (--wine " + wineOverride + ") est WoW64 seul")
	}
	alt := findWin32Wine()
	if alt == "" {
		die("prefix win32, mais "+runnerName(wineBinary)+" est un Wine WoW64 seul",
			"  (pas de lib/wine/i386-unix) qui ne peut pas lancer de prefix 32 bits.",
			"  → Installer un Wine classique (ex. Kron4ek wine-*-amd64, non wow64)",
			"    dans /opt ou ~/.local/share/wsquashfs/wine")
	}
	wineBinary = alt
}

// configureDllOverrides : overrides DLL de Wine direct, ou équivalents Proton.
func configureDllOverrides() {
	if umuRun != "" {
		// Proton pose lui-même DXVK/VKD3D/NVAPI dans le prefix.
		if dxvkWanted == "0" {
			os.Setenv("PROTON_USE_WINED3D", "1")
		}
		dxvk, vkd3d, d7vk = "0", "0", "0"
		// Proton COPIE ~560 DLL par prefix sinon : liens (PROTON_DLL_COPY vide).
		os.Setenv("PROTON_DLL_COPY", os.Getenv("PROTON_DLL_COPY"))
	} else {
		if d7vk == "1" {
			addDllOverride("ddraw=n,b")
		}
		if dxvk == "1" {
			addDllOverride("d3d8,d3d9,d3d10core,d3d11,dxgi=n,b")
		} else {
			addDllOverride("d3d8,d3d9,d3d10core,d3d11,dxgi=b")
		}
		if vkd3d == "1" {
			addDllOverride("d3d12,d3d12core=n,b")
		}
		if nvapiOn == "1" {
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

// applyWineEnv : ENV= de l'autorun, NOM=valeur séparés par des espaces.
func applyWineEnv(env string) {
	for _, pair := range strings.Fields(env) {
		if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`).MatchString(pair) {
			k, v, _ := strings.Cut(pair, "=")
			os.Setenv(k, v)
		}
	}
}

// configureWine : architecture, GPU, environnement Wine du jeu.
func configureWine() {
	if wineArch == "" {
		wineArch = detectPrefixArch()
	}
	ensureWin32Runner()
	detectGpus()
	resolveNvapi()
	resolveVkreflex()
	resolveFsr4()
	configureGpuEnv()

	// Outils Wine (regedit, reg) : par umu pour un prefix Proton.
	wineTool = []string{wineBinary}
	if umuRun != "" {
		wineTool = []string{umuRun}
	}

	os.Setenv("WINEPREFIX", winePoint)
	os.Setenv("WINEARCH", wineArch)

	// Nom d'utilisateur Wine adapté au prefix (steamuser, Batocera).
	prefixUser := detectWineUsername()
	effective := os.Getenv("USER")
	if dropsToTargetUser() {
		effective = targetUser
	}
	if prefixUser != "" && prefixUser != effective {
		os.Setenv("WINEUSERNAME", prefixUser)
	}

	if esync == "1" {
		os.Setenv("WINEESYNC", "1")
	}
	if fsync == "1" {
		os.Setenv("WINEFSYNC", "1")
	}
	configureDllOverrides()

	// Batocera exporte WINEDEBUG=-all ; on garde les err: pour le diagnostic.
	if os.Getenv("WINEDEBUG") == "" {
		os.Setenv("WINEDEBUG", "fixme-all")
	}
	if wineLang != "" {
		os.Setenv("LC_ALL", wineLang)
	}
	if wineEnv != "" {
		applyWineEnv(wineEnv)
	}
}

// ── 9.8 Préparation du prefix ──────────────────────────────────────────

// initPrefixIfNeeded : crée le prefix d'un jeu seul, ou répare un prefix sans
// system32.
func initPrefixIfNeeded() {
	if umuRun != "" {
		if !isFile(winePoint + "/system.reg") {
			fmt.Println("Initialisation du préfixe par Proton au premier lancement...")
		}
		return
	}
	if isFile(winePoint + "/system.reg") {
		if st, err := os.Stat(winePoint + "/drive_c"); err == nil && st.IsDir() &&
			(packagedPrefix || isFile(winePoint+"/drive_c/windows/system32/explorer.exe")) {
			fmt.Println("✓ Préfixe Wine détecté (structure Batocera)")
			return
		}
	}
	fmt.Println("Initialisation du préfixe Wine (" + wineArch + ")...")
	// wineboot -u ne réinstalle rien tant que .update-timestamp correspond.
	_ = os.Remove(winePoint + "/.update-timestamp")
	_ = runWineboot(os.Getenv("WINEDEBUG"))
	_ = runWineserver(winePoint, "-w")
	// Overlay : jamais de chown sur WINEPOINT (le montage), l'upperdir suffit.
	if mountMode == "overlay" {
		chownToTarget(savePoint)
	} else {
		chownToTarget(winePoint)
	}
}

// installPrefixDlls : DLL du runner, DXVK, VKD3D, d7vk et NVAPI (sans umu).
func installPrefixDlls() {
	if umuRun != "" {
		return
	}
	if mountMode == "overlay" && overlayKind == "fuse" {
		linkRuntimeDlls(savePoint)
		chownToTarget(savePoint)
		reloadOverlay("après sync DLLs")
		return
	}
	linkRuntimeDlls(winePoint)
	chownToTarget(winePoint)
}

// importRegistryFiles : regs/*.reg importés à chaque lancement.
func importRegistryFiles() {
	regs := winePoint + "/regs"
	if st, err := os.Stat(regs); err != nil || !st.IsDir() {
		return
	}
	fmt.Println("Import des fichiers de registre...")
	files, _ := filepath.Glob(regs + "/*")
	for _, f := range files {
		if !strings.EqualFold(filepath.Ext(f), ".reg") {
			continue
		}
		fmt.Println("  → " + filepath.Base(f))
		_ = runWineTool("regedit", "//?/unix"+f)
	}
}

// preparePrefix : prefix prêt à lancer.
func preparePrefix() {
	initPrefixIfNeeded()
	if umuRun == "" {
		updatePrefixIfNeeded()
	}
	fixUserdataLinks()
	installPrefixDlls()
	if umuRun == "" {
		writeNvidiaRegistry()
	}
	importRegistryFiles()
}

// ── 9.9 / 9.10 Récapitulatif, commande et exécution ────────────────────

// printSummary : récapitulatif du lancement.
func printSummary() {
	fmt.Println("-----------------------------------------")
	fmt.Println("Exécutable    : " + execPath)
	fmt.Println("Arguments     : " + orEmpty(execArgs, "<aucun>"))
	if umuRun != "" {
		if protonDir+"/files/bin/wine" == imageRunner {
			fmt.Println("Proton        : " + filepath.Base(protonDir) + " via umu (" + protonDir + ") — joint à l'image")
		} else {
			fmt.Println("Proton        : " + filepath.Base(protonDir) + " via umu (" + protonDir + ")")
		}
		if v := runnerVersion(protonDir + "/files/bin/wine"); v != "" {
			fmt.Println("Version Proton: " + v)
		}
	} else {
		if imageRunner != "" && wineBinary == imageRunner {
			fmt.Println("Wine          : " + wineBinary + " (" + wineArch + ") — joint à l'image")
		} else {
			fmt.Println("Wine          : " + wineBinary + " (" + wineArch + ")")
		}
		if v := runnerVersion(wineBinary); v != "" {
			fmt.Println("Version Wine  : " + v)
		}
	}
	fmt.Println("Préfixe       : " + os.Getenv("WINEPREFIX"))
	if u := os.Getenv("WINEUSERNAME"); u != "" {
		fmt.Println("Utilisateur   : " + u + " (adapté au prefix)")
	}
	if runAsRoot {
		if targetUser != "" {
			fmt.Println("Privilège     : montage en root, Wine relancé en " + targetUser)
		} else {
			warn("lancé en root sans SUDO_USER — l'affichage risque d'échouer (pas de cookie X11)")
		}
	}
	if mountMode == "overlay" {
		fmt.Println("Sauvegardes   : " + savePoint)
	}
	fmt.Println("Manettes      : " + hidrawMsg)
	if dxvk == "1" {
		fmt.Println("DXVK          : activé")
	}
	if vkd3d == "1" {
		fmt.Println("VKD3D         : activé")
	}
	if d7vk == "1" {
		fmt.Println("D7VK          : activé (DirectDraw → Vulkan)")
	}
	if esync == "1" {
		fmt.Println("ESYNC         : activé")
	}
	if fsync == "1" {
		fmt.Println("FSYNC         : activé")
	}
	if wineLang != "" {
		fmt.Println("Langue        : " + wineLang)
	}
	if virtualDesktop != "" {
		fmt.Println("Bureau virtuel: " + virtualDesktop)
	}
	fmt.Println("=========================================")
	fmt.Println()
}

// buildRunCommand : ligne de commande du jeu (wine [explorer /desktop] [cmd /c]
// exe args), umu-run à la place de wine, gamescope pour --headless.
func buildRunCommand() {
	runCmdArgs = []string{wineBinary}
	if virtualDesktop != "" {
		desktop := regexp.MustCompile(`[^a-zA-Z0-9_]`).ReplaceAllString(strings.ReplaceAll(gameName, " ", "_"), "")
		runCmdArgs = append(runCmdArgs, "explorer", "/desktop="+desktop+","+virtualDesktop)
	}
	if ext == "bat" {
		runCmdArgs = append(runCmdArgs, "cmd", "/c")
	}
	runCmdArgs = append(runCmdArgs, execPath)
	runCmdArgs = append(runCmdArgs, execArgsArr...)
	if umuRun != "" {
		runCmdArgs[0] = umuRun
	}
	if headless {
		w, h := "1920", "1080"
		if m := regexp.MustCompile(`^([0-9]+)x([0-9]+)$`).FindStringSubmatch(virtualDesktop); m != nil {
			w, h = m[1], m[2]
		}
		runCmdArgs = append([]string{"gamescope", "--backend", "headless", "-w", w, "-h", h, "-W", w, "-H", h, "--"}, runCmdArgs...)
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
	return exec.Command("sudo", append([]string{"-u", targetUser, "env"}, append(env, args...)...)...)
}

// playGame : le jeu lui-même, avec l'utilisateur de la partie.
func playGame() int {
	var cmd *exec.Cmd
	if dropsToTargetUser() {
		cmd = runAsTargetUser(runCmdArgs...)
	} else {
		cmd = asWineUser(runCmdArgs[0], runCmdArgs[1:]...)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if lockFile != nil {
		cmd.ExtraFiles = []*os.File{lockFile}
	}
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
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
	if preCmd == "" {
		return
	}
	// Dossier courant : celui du jeu (runGame vient de s'y placer).
	cmd := asGameUser("bash", "-c", preCmd)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if cmd.Run() != nil {
		die("PRE a échoué : " + preCmd)
	}
}

// gameProcessPID : PID du processus du jeu (nom du noyau, 15 caractères) dont
// l'environnement a WINEPREFIX de ce jeu.
func gameProcessPID(name string) int {
	prefixes := []string{regexp.QuoteMeta(winePoint)}
	if real, err := filepath.EvalSymlinks(winePoint); err == nil && real != winePoint {
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
	if attachCmd == "" {
		return
	}
	if attachProcess == "" {
		attachProcess = filepath.Base(execPath)
	}
	attachMarker = fmt.Sprintf("WSQUASHFS_ATTACH=%d", os.Getpid())
	attachStop = make(chan struct{})
	attachDone = make(chan struct{})
	// Canal local : la variable globale est remise à nil à l'arrêt.
	stop, done := attachStop, attachDone
	go func() {
		defer close(done)
		var pid int
		for {
			select {
			case <-stop:
				// Partie finie avant le processus du jeu (trap USR1 du script).
				if pid == 0 {
					warn("ATTACH : processus " + attachProcess + " introuvable, commande non lancée")
				}
				return
			default:
			}
			if pid = gameProcessPID(attachProcess); pid != 0 {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		cmd := asGameUser("env", attachMarker, "GAME_PID="+strconv.Itoa(pid), "bash", "-c", attachCmd)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}()
}

var (
	attachMarker string
	attachStop   chan struct{} // fin de la partie : abandonne la recherche
	attachDone   chan struct{} // la goroutine ATTACH est terminée
)

// stopAttachCommand : arrête ATTACH et ce qu'elle a lancé (marqueur
// WSQUASHFS_ATTACH). Une commande lancée a 5 s pour finir seule.
func stopAttachCommand() {
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
	os.Chdir(winePoint + ifNotEmpty(dir))
	buildRunCommand()
	if action != "exec" {
		runPreCommand()
		startAttachCommand()
	}
	code := playGame()
	if signalExiting {
		select {} // sortie sur signal en cours : cleanup puis os.Exit
	}
	_ = runWineserver(winePoint, "-w")
	stopAttachCommand()
	fmt.Println()
	fmt.Println("=========================================")
	fmt.Printf("Jeu terminé (code : %d)\n", code)
	fmt.Println("=========================================")
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
	if nsStage != "" {
		// Relancé dans le namespace : verrou hérité, dossiers préparés par l'hôte.
		lockedGame = gameName
		useKernelOverlay()
	} else {
		printLauncherHeader()
		if !acquireGameLock() {
			die(gameName + " est déjà lancé")
		}
		if dirMode {
			useInstalledDir()
		} else {
			mountImage()
		}
	}
	if action == "mount" {
		reportMountAndExit()
	}
	readAutorun()
	resolveExecutable()
	selectRunner()
	configureWine()
	preparePrefix()
	if action == "prepare" {
		applySavedir(savedirKey, savefilesKey)
		fmt.Println("✓ Prefix préparé : " + gameName + " (jeu non lancé)")
		exitNow(0)
	}
	configureControllers()
	applySavedir(savedirKey, savefilesKey)
	printSummary()
	runGame()
}

// printLauncherHeader : en-tête du lancement.
func printLauncherHeader() {
	fmt.Println("=========================================")
	fmt.Println("WSquashFS Launcher")
	fmt.Println("=========================================")
	fmt.Println("Environnement : " + detectEnv())
	fmt.Println("Jeu           : " + gameName)
	if v := gameVersion(wsquashfsFile); v != "" {
		fmt.Println("Version       : " + v)
	}
	fmt.Println("Fichier       : " + wsquashfsFile)
	if f := machineLayerFile(); isFile(f) {
		fmt.Println("Réglages mach.: " + f + " (" + overrideKeys(f) + ")")
	}
	if f := filepath.Dir(strings.TrimSuffix(wsquashfsFile, "/")) + "/_defaults.autorun"; isFile(f) {
		fmt.Println("Réglages dos. : " + f + " (" + overrideKeys(f) + ")")
	}
	if autorunOverride != "" {
		fmt.Println("Surcharge     : " + filepath.Base(autorunOverride) + " (" + overrideKeys(autorunOverride) + ")")
	}
	if len(setArgs) > 0 {
		fmt.Println("Options --set : " + strings.Join(setArgs, " "))
	}
	if action != "run" {
		name := ""
		if len(execCmd) > 0 {
			name = " (" + execCmd[0] + ")"
		}
		fmt.Println("Action        : " + action + name)
	}
}
