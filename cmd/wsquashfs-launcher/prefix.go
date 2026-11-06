package main

// Préparation du prefix : commandes Wine, DLL du runner, DXVK, VKD3D, d7vk,
// NVAPI, FSR4, environnement GPU, sauvegardes Batocera (script bash, section 6).

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

// asWineUser : commande lancée en utilisateur Wine. Overlay noyau : sans les
// capacités du namespace (le bwrap de Proton refuse de démarrer avec des
// capacités sans être setuid).
func asWineUser(name string, args ...string) *exec.Cmd {
	if dropsToTargetUser() {
		return exec.Command("sudo", append([]string{"-u", targetUser, name}, args...)...)
	}
	if nsStage != "" {
		return exec.Command("setpriv", append([]string{"--inh-caps=-all", "--ambient-caps=-all", name}, args...)...)
	}
	return exec.Command(name, args...)
}

// runWineserver : wineserver du prefix, lancé en utilisateur Wine (celui du
// runner choisi, sinon celui du PATH).
func runWineserver(prefix string, args ...string) error {
	server := "wineserver"
	if wineBinary != "" && isExecutable(filepath.Dir(wineBinary)+"/wineserver") {
		server = filepath.Dir(wineBinary) + "/wineserver"
	}
	cmd := asWineUser("env", append([]string{"WINEPREFIX=" + prefix, server}, args...)...)
	return cmd.Run()
}

// runWineboot : crée ou met à jour le prefix.
func runWineboot(winedebug string) error {
	cmd := asWineUser("env", "DISPLAY="+os.Getenv("DISPLAY"), "WINEPREFIX="+winePoint, "WINEARCH="+wineArch,
		"WINEDEBUG="+winedebug, "WINEDLLOVERRIDES=mscoree,mshtml=;winemenubuilder.exe=d",
		wineBinary, "wineboot", "-u")
	return cmd.Run()
}

// runWineTool : outil Wine (regedit, reg) sur le prefix, par umu pour un prefix Proton.
func runWineTool(args ...string) error {
	full := append([]string{"env", "DISPLAY=" + os.Getenv("DISPLAY"), "WINEPREFIX=" + winePoint, "WINEARCH=" + wineArch,
		"WINEDEBUG=" + os.Getenv("WINEDEBUG"), "WINEDLLOVERRIDES=" + os.Getenv("WINEDLLOVERRIDES"),
		"GAMEID=" + os.Getenv("GAMEID"), "PROTONPATH=" + os.Getenv("PROTONPATH")}, wineTool...)
	full = append(full, args...)
	return asWineUser(full[0], full[1:]...).Run()
}

// stopWineserver : arrêt propre (kill, wait, puis kill -9).
func stopWineserver() {
	_ = runWineserver(winePoint, "-k")
	for i := 0; i < 10; i++ {
		if runWineserver(winePoint, "-w") == nil {
			return
		}
		time.Sleep(time.Second)
	}
	killByEnv("WINEPREFIX=" + winePoint)
}

// killByEnv : kill -9 des processus dont l'environnement contient needle
// (pgrep -f en bash, ici via /proc).
func killByEnv(needle string) {
	matches, _ := filepath.Glob("/proc/[0-9]*/environ")
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil || !strings.Contains(string(data), needle) {
			continue
		}
		var pid int
		fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(m, "/proc/"), "/environ"), "%d", &pid)
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// detectPrefixArch : architecture réelle du prefix (ligne #arch=win32).
func detectPrefixArch() string {
	for _, f := range []string{winePoint + "/system.reg", winePoint + "/userdef.reg"} {
		data, err := os.ReadFile(f)
		if err == nil && strings.Contains("\n"+string(data), "\n#arch=win32") {
			return "win32"
		}
	}
	return "win64"
}

// detectWineUsername : nom d'utilisateur Wine du prefix (packagé par Batocera :
// steamuser), premier hors comptes système.
func detectWineUsername() string {
	entries, err := os.ReadDir(winePoint + "/drive_c/users")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		switch e.Name() {
		case "Public", "All Users", "Default User", "LocalService", "NetworkService":
			continue
		}
		if e.IsDir() {
			return e.Name()
		}
	}
	return ""
}

var wineOwnedRe = regexp.MustCompile(`Wine (builtin|placeholder) DLL`)

// syncWineStubs : liens symboliques vers les DLL du runner, à la place des DLL
// internes de Wine (les DLL natives de Microsoft sont gardées). Écrit dans
// targetRoot (SAVEPOINT en overlay, WINEPOINT sinon).
func syncWineStubs(wineBin, targetRoot string) {
	wineRoot := runnerRoot(wineBin)
	type srcDir struct{ src, dst string }
	srcs := []srcDir{
		{wineRoot + "/lib/wine/x86_64-windows", "system32"},
		{wineRoot + "/lib64/wine/x86_64-windows", "system32"},
		{wineRoot + "/lib/wine/i386-windows", "syswow64"},
		{sysPath("/usr/lib/x86_64-linux-gnu/wine/x86_64-windows"), "system32"},
		{sysPath("/usr/lib/x86_64-linux-gnu/wine/i386-windows"), "syswow64"},
		{sysPath("/usr/lib/i386-linux-gnu/wine/i386-windows"), "syswow64"},
	}
	for _, e := range srcs {
		dstSub := e.dst
		// Prefix win32 : seules les DLL 32 bits, toutes dans system32.
		if wineArch == "win32" {
			if dstSub == "system32" {
				continue
			}
			dstSub = "system32"
		}
		st, err := os.Stat(e.src)
		if err != nil || !st.IsDir() {
			continue
		}
		dstDir := targetRoot + "/drive_c/windows/" + dstSub
		_ = os.MkdirAll(dstDir, 0o755)
		owned := map[string]bool{}
		if files, _ := filepath.Glob(winePoint + "/drive_c/windows/" + dstSub + "/*"); files != nil {
			for _, f := range files {
				if data, err := os.ReadFile(f); err == nil && wineOwnedRe.Match(data) {
					owned[filepath.Base(f)] = true
				}
			}
		}
		srcFiles, _ := filepath.Glob(e.src + "/*")
		for _, src := range srcFiles {
			if st, err := os.Stat(src); err != nil || !st.Mode().IsRegular() {
				continue
			}
			name := filepath.Base(src)
			existing := winePoint + "/drive_c/windows/" + dstSub + "/" + name
			if isFile(existing) && !owned[name] {
				continue
			}
			link := dstDir + "/" + name
			_ = os.Remove(link)
			_ = os.Symlink(src, link)
		}
	}
}

// linkDllSet : relie <src>/<dir64>/<dll>.dll dans system32 et <dir32> dans
// syswow64 ; prefix win32 : 32 bits seulement, dans system32.
func linkDllSet(srcDir, dir64, dir32, targetRoot string, dlls ...string) {
	for _, pair := range [][2]string{{dir64, "system32"}, {dir32, "syswow64"}} {
		arch, sub := pair[0], pair[1]
		if wineArch == "win32" {
			if arch == dir64 {
				continue
			}
			sub = "system32"
		}
		_ = os.MkdirAll(targetRoot+"/drive_c/windows/"+sub, 0o755)
		for _, dll := range dlls {
			src := srcDir + "/" + arch + "/" + dll + ".dll"
			if !isFile(src) {
				continue
			}
			link := targetRoot + "/drive_c/windows/" + sub + "/" + dll + ".dll"
			_ = os.Remove(link)
			_ = os.Symlink(src, link)
		}
	}
}

// findFirstDir : premier dossier de dirs qui contient probe (chemin absolu).
func findFirstDir(probe string, dirs ...string) string {
	for _, d := range dirs {
		if d != "" && isFile(d+"/"+probe) {
			return d
		}
	}
	return ""
}

func findDxvkDir() string {
	return findFirstDir("x64/dxgi.dll", os.Getenv("WSQUASHFS_DXVK_DIR"), sysPath("/usr/share/dxvk"), filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/dxvk"))
}

// installDxvk : liens DXVK recréés à chaque lancement (après syncWineStubs).
func installDxvk(target string) {
	if d := findDxvkDir(); d != "" {
		linkDllSet(d, "x64", "x32", target, "d3d8", "d3d9", "d3d10core", "d3d11", "dxgi")
	}
}

func findVkd3dDir() string {
	return findFirstDir("x64/d3d12.dll", os.Getenv("WSQUASHFS_VKD3D_DIR"), sysPath("/usr/share/vkd3d-proton"), filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/vkd3d-proton"))
}

func installVkd3d(target string) {
	if d := findVkd3dDir(); d != "" {
		linkDllSet(d, "x64", "x86", target, "d3d12", "d3d12core")
	}
}

// findD7vk : ddraw.dll 32 bits de d7vk.
func findD7vk() string {
	for _, f := range []string{os.Getenv("WSQUASHFS_D7VK_DLL"), sysPath("/usr/share/d7vk/x32/ddraw.dll"),
		filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/d7vk/x32/ddraw.dll")} {
		if f != "" && isFile(f) {
			return f
		}
	}
	return ""
}

func installD7vk(target string) {
	d7 := findD7vk()
	if d7 == "" {
		return
	}
	sub := "syswow64"
	if wineArch == "win32" {
		sub = "system32"
	}
	wineRoot := runnerRoot(wineBinary)
	builtin := ""
	for _, c := range []string{wineRoot + "/lib/wine/i386-windows/ddraw.dll", wineRoot + "/lib32/wine/i386-windows/ddraw.dll",
		sysPath("/usr/lib/wine/i386-windows/ddraw.dll")} {
		if isFile(c) {
			builtin = c
			break
		}
	}
	_ = os.MkdirAll(target+"/drive_c/windows/"+sub, 0o755)
	link := target + "/drive_c/windows/" + sub + "/ddraw.dll"
	_ = os.Remove(link)
	_ = os.Symlink(d7, link)
	if builtin != "" {
		l2 := target + "/drive_c/windows/" + sub + "/ddraw_.dll"
		_ = os.Remove(l2)
		_ = os.Symlink(builtin, l2)
	}
}

// ── GPU : AMD et NVIDIA ────────────────────────────────────────────────

// detectGpus : fournisseurs présents d'après l'ID PCI (0x10de NVIDIA, 0x1002 AMD).
func detectGpus() {
	hasNvidia, hasAMD = "0", "0"
	vendors, _ := filepath.Glob(envOr("WSQUASHFS_DRM_SYSFS", "/sys/class/drm") + "/card*/device/vendor")
	for _, v := range vendors {
		data, err := os.ReadFile(v)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(data)) {
		case "0x10de":
			hasNvidia = "1"
		case "0x1002":
			hasAMD = "1"
		}
	}
}

func findNvapiDir() string {
	return findFirstDir("x64/nvapi64.dll", os.Getenv("WSQUASHFS_NVAPI_DIR"), sysPath("/usr/share/dxvk-nvapi"), filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/dxvk-nvapi"))
}

// resolveNvapi : NVAPI posée seulement si demandée (NVAPI=1, VKREFLEX=1), une
// carte NVIDIA et dxvk-nvapi présents. auto : décidé sur la machine.
func resolveNvapi() {
	nvapiOn = "0"
	want := nvapi
	if vkreflex == "1" {
		want = "1"
	}
	if want == "0" {
		return
	}
	if hasNvidia != "1" {
		if want == "1" {
			warn("NVAPI=1 ignoré : aucune carte NVIDIA détectée")
		}
	} else if findNvapiDir() == "" {
		if want == "1" {
			warn("NVAPI=1 ignoré : dxvk-nvapi introuvable (/usr/share/dxvk-nvapi)")
		}
	} else {
		nvapiOn = "1"
	}
}

// resolveVkreflex : couche Reflex de dxvk-nvapi.
func resolveVkreflex() {
	vkreflexOn = "0"
	if vkreflex == "0" || nvapiOn != "1" {
		return
	}
	nvDir := findNvapiDir()
	if isFile(nvDir + "/layer/VkLayer_DXVK_NVAPI_reflex.json") {
		vkreflexOn = "1"
	} else if vkreflex == "1" {
		warn("VKREFLEX=1 ignoré : couche Reflex absente (" + nvDir + "/layer)")
	}
}

// linkRunnerComponents : icu (et vkd3d si VKD3D=1) des runners qui rangent ces
// DLL hors de lib/wine (GE-Proton, wine-ge).
func linkRunnerComponents(target string) {
	wineRoot := runnerRoot(wineBinary)
	comps := []string{"icu"}
	if vkd3d == "1" {
		comps = []string{"icu", "vkd3d"}
	}
	for _, comp := range comps {
		for _, arch := range []string{"x86_64-windows", "i386-windows"} {
			sub := "system32"
			if arch == "x86_64-windows" {
				sub = "system32"
			} else {
				sub = "syswow64"
			}
			if wineArch == "win32" {
				if arch == "x86_64-windows" {
					continue
				}
				sub = "system32"
			}
			d := ""
			for _, c := range []string{wineRoot + "/lib/wine/" + comp + "/" + arch, wineRoot + "/lib64/wine/" + comp + "/" + arch,
				wineRoot + "/lib/" + comp + "/" + arch, wineRoot + "/lib64/" + comp + "/" + arch} {
				if st, err := os.Stat(c); err == nil && st.IsDir() {
					d = c
					break
				}
			}
			if d == "" {
				continue
			}
			_ = os.MkdirAll(target+"/drive_c/windows/"+sub, 0o755)
			files, _ := filepath.Glob(d + "/*.dll")
			for _, f := range files {
				dst := target + "/drive_c/windows/" + sub + "/" + filepath.Base(f)
				if st, err := os.Lstat(dst); err == nil && st.Mode()&os.ModeSymlink == 0 {
					continue
				}
				_ = os.Remove(dst)
				_ = os.Symlink(f, dst)
			}
		}
	}
}

// findFsr4Dll : amdxcffx64.dll (FSR4_DIR, paquet système, ou copie par l'installeur).
func findFsr4Dll() string {
	for _, d := range []string{os.Getenv("WSQUASHFS_FSR4_DIR"), sysPath("/usr/share/fsr4"), filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/fsr4")} {
		if d != "" && isFile(d+"/amdxcffx64.dll") {
			return d + "/amdxcffx64.dll"
		}
	}
	return ""
}

// resolveFsr4 : FSR4 seulement si FSR4=1, une carte AMD et la DLL présentes.
func resolveFsr4() {
	fsr4On = "0"
	if fsr4 != "1" {
		return
	}
	if hasAMD != "1" {
		warn("FSR4=1 ignoré : aucune carte AMD détectée")
	} else if findFsr4Dll() == "" {
		warn("FSR4=1 ignoré : amdxcffx64.dll introuvable (install.sh propose de la copier depuis proton-cachyos)")
	} else {
		fsr4On = "1"
	}
}

// installFsr4 : lien FSR4 posé ; retiré quand FSR4 n'est plus demandé.
func installFsr4(target string) {
	link := target + "/drive_c/windows/system32/amdxcffx64.dll"
	if fsr4On != "1" {
		if st, err := os.Lstat(link); err == nil && st.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(link)
		}
		return
	}
	dll := findFsr4Dll()
	if dll == "" {
		return
	}
	_ = os.MkdirAll(target+"/drive_c/windows/system32", 0o755)
	_ = os.Remove(link)
	_ = os.Symlink(dll, link)
}

// installNvapi : dxvk-nvapi (NVAPI, DLSS) et nvngx.dll du pilote NVIDIA.
func installNvapi(target string) {
	nv := findNvapiDir()
	if nv == "" {
		return
	}
	sub32 := "syswow64"
	if wineArch == "win32" {
		sub32 = "system32"
	}
	_ = os.MkdirAll(target+"/drive_c/windows/system32", 0o755)
	_ = os.MkdirAll(target+"/drive_c/windows/"+sub32, 0o755)
	link := func(src, dst string) {
		_ = os.Remove(dst)
		_ = os.Symlink(src, dst)
	}
	if wineArch != "win32" {
		link(nv+"/x64/nvapi64.dll", target+"/drive_c/windows/system32/nvapi64.dll")
		if isFile(nv + "/x64/nvofapi64.dll") {
			link(nv+"/x64/nvofapi64.dll", target+"/drive_c/windows/system32/nvofapi64.dll")
		}
	}
	if isFile(nv + "/x32/nvapi.dll") {
		link(nv+"/x32/nvapi.dll", target+"/drive_c/windows/"+sub32+"/nvapi.dll")
	}
	ngx := sysPath("/usr/lib/nvidia/wine")
	if wineArch != "win32" && isFile(ngx+"/nvngx.dll") {
		for _, f := range []string{"_nvngx.dll", "nvngx.dll", "nvngx_dlssg.dll"} {
			if isFile(ngx + "/" + f) {
				link(ngx+"/"+f, target+"/drive_c/windows/system32/"+f)
			}
		}
	}
}

// linkRuntimeDlls : DLL du runner, DXVK, VKD3D, d7vk, NVAPI, FSR4.
func linkRuntimeDlls(target string) {
	syncWineStubs(wineBinary, target)
	linkRunnerComponents(target)
	if dxvk == "1" {
		installDxvk(target)
	}
	if vkd3d == "1" {
		installVkd3d(target)
	}
	if d7vk == "1" {
		installD7vk(target)
	}
	if nvapiOn == "1" {
		installNvapi(target)
	}
	installFsr4(target)
}

// writeNvidiaRegistry : clé NGXCore pour DLSS (une fois par prefix).
func writeNvidiaRegistry() {
	if nvapiOn != "1" || !isFile(sysPath("/usr/lib/nvidia/wine/nvngx.dll")) {
		return
	}
	if data, err := os.ReadFile(winePoint + "/system.reg"); err == nil && strings.Contains(string(data), "NGXCore") {
		return
	}
	_ = runWineTool("reg", "add", `HKLM\Software\NVIDIA Corporation\Global\NGXCore`,
		"/v", "FullPath", "/t", "REG_SZ", "/d", `C:\Windows\System32`, "/f")
	_ = runWineserver(winePoint, "-k")
	_ = runWineserver(winePoint, "-w")
}

// configureGpuEnv : caches de shaders et journaux, réglés pour tous les GPU.
func configureGpuEnv() {
	shaderDir := cacheDir + "/shaders"
	_ = os.MkdirAll(shaderDir+"/dxvk", 0o755)
	_ = os.MkdirAll(shaderDir+"/vkd3d", 0o755)
	chownToTarget(shaderDir)
	os.Setenv("DXVK_STATE_CACHE_PATH", shaderDir+"/dxvk")
	os.Setenv("VKD3D_SHADER_CACHE_PATH", shaderDir+"/vkd3d")
	os.Setenv("DXVK_LOG_LEVEL", envOr("DXVK_LOG_LEVEL", "none"))
	os.Setenv("VKD3D_DEBUG", envOr("VKD3D_DEBUG", "none"))
	os.Setenv("WINE_MONO_OVERRIDES", "Microsoft.Xna.Framework.*,Gac=n")
	if fsr == "1" {
		os.Setenv("WINE_FULLSCREEN_FSR", "1")
	} else {
		os.Setenv("WINE_FULLSCREEN_FSR", "0")
	}
	// Couche Reflex : Proton charge la sienne sous umu.
	if vkreflexOn == "1" {
		os.Setenv("DXVK_NVAPI_VKREFLEX", "1")
		if umuRun == "" {
			add := findNvapiDir() + "/layer"
			if cur := os.Getenv("VK_ADD_IMPLICIT_LAYER_PATH"); cur != "" {
				add = cur + ":" + add
			}
			os.Setenv("VK_ADD_IMPLICIT_LAYER_PATH", add)
		}
	} else if umuRun == "" {
		os.Setenv("DISABLE_DXVK_NVAPI_VKREFLEX", "1")
	}
	if hasNvidia == "1" {
		nvDir := shaderDir + "/nvidia"
		_ = os.MkdirAll(nvDir, 0o755)
		chownToTarget(nvDir)
		os.Setenv("__GL_SHADER_DISK_CACHE_PATH", nvDir)
		os.Setenv("__GL_SHADER_DISK_CACHE_SIZE", "2147483648")
		os.Setenv("__GL_SHADER_DISK_CACHE_SKIP_CLEANUP", "1")
	}
}

// updatePrefixIfNeeded : mise à jour du prefix au changement de runner
// (comme createOrUpdatePrefix de batocera-wine).
func updatePrefixIfNeeded() {
	if !isFile(winePoint + "/system.reg") {
		return
	}
	wineRoot := runnerRoot(wineBinary)
	inf := wineRoot + "/share/wine/wine.inf"
	if !isFile(inf) {
		return
	}
	st, err := os.Stat(inf)
	if err != nil {
		return
	}
	ts := fmt.Sprint(st.ModTime().Unix())
	stamp := strings.TrimRight(strings.SplitN(readTrim(winePoint+"/.update-timestamp"), "\n", 2)[0], "\r")
	if stamp == "disable" || stamp == ts {
		return
	}
	version := strings.TrimSpace(runVersion(wineBinary))
	if version == "" {
		version = filepath.Base(wineRoot)
	}
	fmt.Println("Préfixe       : mise à jour pour " + version)
	for _, d := range []string{"system32", "syswow64"} {
		links, _ := filepath.Glob(winePoint + "/drive_c/windows/" + d + "/*")
		for _, l := range links {
			if lst, err := os.Lstat(l); err == nil && lst.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(l)
			}
		}
	}
	_ = runWineboot("-all")
	_ = runWineserver(winePoint, "-w")
	// wineboot -u vient d'écrire ~1 600 DLL dans l'upperdir : retirées, puis
	// remplacées par des liens (prefix packagé seulement).
	if mountMode == "overlay" && packagedPrefix {
		_ = os.RemoveAll(savePoint + "/drive_c/windows/system32")
		_ = os.RemoveAll(savePoint + "/drive_c/windows/syswow64")
		reloadOverlay("après mise à jour du préfixe")
	}
}

// runVersion : sortie de wine --version (vide si échec).
func runVersion(bin string) string {
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.SplitN(string(out), "\n", 2)[0]
}

// fixUserdataLinks : les dossiers du profil liés à /userdata (absent ici)
// deviennent de vrais dossiers, dans l'upperdir en overlay.
func fixUserdataLinks() {
	users := winePoint + "/drive_c/users"
	_ = filepath.Walk(users, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		if rel, _ := filepath.Rel(users, p); strings.Count(rel, string(os.PathSeparator)) >= 6 {
			return filepath.SkipDir
		}
		target, _ := os.Readlink(p)
		if strings.HasPrefix(target, "/userdata") {
			_ = os.Remove(p)
			_ = os.MkdirAll(p, 0o755)
		}
		return nil
	})
}

// batoceraSavesDir : emplacement des sauvegardes Batocera
// (<racine>/saves/<système>/<jeu>) ; faux hors d'un dossier roms/<système>/.
func batoceraSavesDir() (string, bool) {
	if v := os.Getenv("WSQUASHFS_BATOCERA_SAVES"); v != "" {
		return v + "/" + gameName, true
	}
	romDir := filepath.Dir(wsquashfsFile)
	sys := filepath.Base(romDir)
	root := filepath.Dir(filepath.Dir(romDir))
	if filepath.Base(filepath.Dir(romDir)) != "roms" {
		return "", false
	}
	return root + "/saves/" + sys + "/" + gameName, true
}

// applySavedir : SAVEDIR / SAVEFILES, remplacés par des liens vers les
// sauvegardes Batocera (copie initiale sans écraser).
func applySavedir(savedir, savefiles string) {
	if savedir == "" && savefiles == "" {
		return
	}
	sysdir, ok := batoceraSavesDir()
	if !ok {
		warn("SAVEDIR/SAVEFILES ignorés (jeu hors d'un dossier roms/<système>/ ;",
			"  définir WSQUASHFS_BATOCERA_SAVES)")
		return
	}
	_ = os.MkdirAll(sysdir, 0o755)
	if savedir != "" {
		savedir = strings.ReplaceAll(savedir, `\`, "/")
		parent := winePoint + "/" + filepath.Dir(savedir)
		base := filepath.Base(savedir)
		_ = os.MkdirAll(parent, 0o755)
		p := parent + "/" + base
		if st, err := os.Lstat(p); err == nil && st.IsDir() {
			_ = runCmd("cp", "-an", p+"/.", sysdir+"/")
			_ = os.RemoveAll(p)
		}
		if st, err := os.Lstat(p); err != nil || st.Mode()&os.ModeSymlink == 0 {
			_ = os.Symlink(sysdir, p)
		}
	} else {
		for _, f := range strings.Split(strings.ReplaceAll(savefiles, `\`, "/"), ";") {
			if f == "" {
				continue
			}
			_ = os.MkdirAll(filepath.Dir(sysdir+"/"+f), 0o755)
			_ = os.MkdirAll(filepath.Dir(winePoint+"/"+f), 0o755)
			target := winePoint + "/" + f
			if st, err := os.Lstat(target); err == nil && st.Mode()&os.ModeSymlink == 0 && st.Mode().IsRegular() {
				if _, err := os.Lstat(sysdir + "/" + f); err != nil {
					_ = os.Rename(target, sysdir+"/"+f)
				}
				_ = os.Remove(target)
			}
			_ = os.Remove(target)
			_ = os.Symlink(sysdir+"/"+f, target)
		}
	}
	chownToTarget(sysdir)
	fmt.Println("Sauvegardes   : " + sysdir + " (SAVEDIR/SAVEFILES)")
}
