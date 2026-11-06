package main

// Runners Wine et Proton, umu, DXVK, VKD3D-Proton, d7vk, NVAPI et FSR4
// (install.sh, sections 5 à 7).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ── Runners Wine (section 5) ───────────────────────────────────────────

// installWineArchive : télécharge et installe un runner Wine dans
// wineInstallDir. Vrai si le binaire wine est en place.
func installWineArchive(url, file, label, name string) bool {
	tmp, err := mkTempDir()
	if err != nil {
		return false
	}
	defer os.RemoveAll(tmp)
	if fetch(url, filepath.Join(tmp, file), label) == nil && dryRun {
		return true
	}
	archive := filepath.Join(tmp, file)
	if isFile(archive) && os.MkdirAll(filepath.Join(tmp, "x"), 0o755) == nil &&
		extract(archive, filepath.Join(tmp, "x")) == nil {
		top := topDirOf(filepath.Join(tmp, "x"))
		if top != "" {
			if name == "" {
				name = filepath.Base(top)
			}
			dest := filepath.Join(wineInstallDir, name)
			if installTree(top, dest) == nil {
				wine := findIn([]string{dest}, 2, isWineBin(""))
				if isExecutable(wine) {
					printSuccess(label + " installé : " + wine)
					return true
				}
			}
		}
	}
	printError(label + " : installation échouée")
	return false
}

// isWineBin : match sur un chemin …/bin/wine contenant sub (vide : tous).
func isWineBin(sub string) func(string) bool {
	return func(p string) bool {
		p = filepath.ToSlash(p)
		return filepath.Base(p) == "wine" && strings.HasSuffix(p, "/bin/wine") &&
			(sub == "" || strings.Contains(strings.ToLower(p), sub))
	}
}

// downloadWineTkg : Wine par défaut des jeux arcade (Kron4ek, non WoW64).
func downloadWineTkg() bool {
	version := githubLatestTag("Kron4ek/Wine-Builds", "11.19")
	file := fmt.Sprintf("wine-%s-staging-tkg-amd64.tar.xz", version)
	url := "https://github.com/Kron4ek/Wine-Builds/releases/download/" + version + "/" + file
	return installWineArchive(url, file, "wine-tkg "+version+" (~100 Mo)", "")
}

func downloadWineGE() bool {
	tag := githubLatestTag("GloriousEggroll/wine-ge-custom", "GE-Proton8-26")
	file := "wine-lutris-" + tag + "-x86_64.tar.xz"
	url := "https://github.com/GloriousEggroll/wine-ge-custom/releases/download/" + tag + "/" + file
	return installWineArchive(url, file, "wine-ge "+tag+" (~234 Mo)", "wine-ge-"+tag)
}

var geRe = regexp.MustCompile(`(?i)ge-|lutris|wine-proton`)

// checkWineRunners : Wine de référence des jeux arcade.
func checkWineRunners() {
	section("Wine pour les jeux arcade Batocera")
	foundTkg := findIn([]string{wineInstallDir, "/opt", "/usr/wine/wine-tkg"}, 3,
		isWineBin("tkg"))
	foundGE := findIn([]string{wineInstallDir, "/usr/wine/ge-custom", "/usr/wine/wine-proton"}, 3,
		func(p string) bool { return isWineBin("")(p) && geRe.MatchString(p) })
	needTkg, needGE := foundTkg == "", foundGE == ""
	if !needTkg {
		printSuccess("wine-tkg : " + foundTkg)
	}
	if !needGE {
		printSuccess("wine-ge  : " + foundGE)
	}
	if !needTkg && !needGE {
		return
	}
	fmt.Println("    Les prefixes .wsquashfs Batocera sont conçus pour wine-tkg ou wine-ge ;")
	fmt.Println("    le Wine système peut provoquer des incompatibilités de DLL.")
	if needTkg {
		fmt.Println("    wine-tkg  manquant  (Kron4ek staging, ~100 Mo) — runner par défaut")
	}
	if needGE {
		fmt.Println("    wine-ge   manquant  (GloriousEggroll, ~234 Mo) — alternative")
	}
	fmt.Println("    1) Installer ce qui manque   2) wine-tkg seulement   3) wine-ge seulement   4) Ignorer")
	switch askChoice("Choix [1-4] :", "2") {
	case "1":
		if needTkg {
			downloadWineTkg()
		}
		if needGE {
			downloadWineGE()
		}
	case "2":
		if needTkg {
			downloadWineTkg()
		}
	case "3":
		if needGE {
			downloadWineGE()
		}
	default:
		printInfo("Ignoré : le Wine système sera utilisé (certaines DLL peuvent manquer)")
	}
}

// ── Proton et umu (section 6) ──────────────────────────────────────────

// findProton : Proton préféré du lanceur (proton-cachyos, puis UMU-Proton).
func findProton() string {
	home := os.Getenv("HOME")
	bases := []string{
		protonInstallDir,
		filepath.Join(home, ".steam/root/compatibilitytools.d"),
		filepath.Join(home, ".config/heroic/tools/proton"),
		"/usr/share/steam/compatibilitytools.d",
	}
	for _, base := range bases {
		for _, pattern := range []string{"proton-cachyos*", "UMU-Proton*"} {
			dirs, _ := filepath.Glob(filepath.Join(base, pattern))
			for _, d := range dirs {
				if isExecutable(filepath.Join(d, "proton")) && isExecutable(filepath.Join(d, "files/bin/wine")) {
					return d
				}
			}
		}
	}
	return ""
}

// downloadUmu : umu-run en zipapp, dans umuInstallDir.
func downloadUmu() bool {
	if _, err := exec.LookPath("python3"); err != nil {
		printError("python3 requis pour umu-run")
		return false
	}
	tag := githubLatestTag("Open-Wine-Components/umu-launcher", "1.4.4")
	file := "umu-launcher-" + tag + "-zipapp.tar"
	url := "https://github.com/Open-Wine-Components/umu-launcher/releases/download/" + tag + "/" + file
	tmp, err := mkTempDir()
	if err != nil {
		return false
	}
	defer os.RemoveAll(tmp)
	if fetch(url, filepath.Join(tmp, file), "umu-launcher "+tag+" (< 1 Mo)") == nil && dryRun {
		return true
	}
	archive := filepath.Join(tmp, file)
	if isFile(archive) && runCmd("tar", "-xf", archive, "-C", tmp) == nil && isFile(filepath.Join(tmp, "umu/umu-run")) {
		os.RemoveAll(umuInstallDir)
		link := filepath.Join(installDir, "umu-run")
		target := filepath.Join(umuInstallDir, "umu-run")
		if os.MkdirAll(umuInstallDir, 0o755) == nil && os.MkdirAll(installDir, 0o755) == nil &&
			copyFile(filepath.Join(tmp, "umu/umu-run"), target, 0o755) == nil {
			os.Remove(link)
			if os.Symlink(target, link) == nil {
				printSuccess("umu-run " + tag + " installé : " + link)
				return true
			}
		}
	}
	printError("umu-launcher : installation échouée")
	return false
}

func installUmu() {
	if distro == "arch" && pacmanSynced() && pacmanAvailable("umu-launcher") {
		_ = pacmanEnsure("umu-launcher", "umu-launcher")
		return
	}
	downloadUmu()
}

// downloadUmuProton : UMU-Proton (SHA-512 vérifiée), dans protonInstallDir.
func downloadUmuProton() bool {
	tag := githubLatestTag("Open-Wine-Components/umu-proton", "UMU-Proton-10.0-4")
	base := "https://github.com/Open-Wine-Components/umu-proton/releases/download/" + tag
	tmp, err := mkTempDir()
	if err != nil {
		return false
	}
	defer os.RemoveAll(tmp)
	if fetch(base+"/"+tag+".tar.gz", filepath.Join(tmp, tag+".tar.gz"), tag+" (~470 Mo)") == nil && dryRun {
		return true
	}
	if !isFile(filepath.Join(tmp, tag+".tar.gz")) || downloadTo(base+"/"+tag+".sha512sum", filepath.Join(tmp, tag+".sha512sum")) != nil {
		return false
	}
	check := exec.Command("sha512sum", "-c", "--quiet", tag+".sha512sum")
	check.Dir = tmp
	if check.Run() != nil {
		printError("Somme de contrôle SHA-512 incorrecte, archive ignorée")
		return false
	}
	x := filepath.Join(tmp, "x")
	if os.MkdirAll(x, 0o755) == nil && extract(filepath.Join(tmp, tag+".tar.gz"), x) == nil {
		top := topDirOf(x)
		if top != "" && isExecutable(filepath.Join(top, "proton")) {
			dest := filepath.Join(protonInstallDir, filepath.Base(top))
			if installTree(top, dest) == nil {
				printSuccess(tag + " installé : " + dest)
				return true
			}
		}
	}
	printError(tag + " : extraction échouée (archive corrompue ?)")
	return false
}

func installProton() {
	if distro == "arch" && pacmanSynced() {
		for _, pkg := range []string{"proton-cachyos-slr", "proton-cachyos"} {
			if pacmanAvailable(pkg) {
				_ = pacmanEnsure(pkg, pkg)
				return
			}
		}
	}
	downloadUmuProton()
}

func checkProton() {
	section("Proton et umu (jeux PC récents)")
	umu, _ := exec.LookPath("umu-run")
	proton := findProton()
	if umu != "" {
		printSuccess("umu-run : " + umu)
	}
	if proton != "" {
		printSuccess("Proton  : " + proton)
	}
	if umu != "" && proton != "" {
		return
	}
	fmt.Println("    Les .wsquashfs sans prefix (jeux PC emballés seuls) et les prefixes")
	fmt.Println("    créés par Proton (Heroic, Steam) se lancent via umu et Proton.")
	fmt.Println("    Inutile pour les seuls jeux arcade Batocera (wine-tkg).")
	if umu == "" {
		fmt.Println("    umu-run manquant")
	}
	if proton == "" {
		fmt.Println("    Proton manquant (proton-cachyos ou UMU-Proton)")
	}
	if askYesNo("Installer ce qui manque ?", true) {
		if umu == "" {
			installUmu()
		}
		if proton == "" {
			installProton()
		}
		return
	}
	printInfo("Sans umu, ces jeux seront lancés par le Wine de Proton seul (écran noir possible)")
}

// ── Rendu : DXVK, VKD3D-Proton, d7vk (section 7) ───────────────────────

// installDllPack : archive d'un composant de rendu (pattern : {tag} et {ver}
// remplacés), installée dans dest ; probe est un fichier témoin.
func installDllPack(label, repo, fallback, pattern, dest, probe string) bool {
	tag := githubLatestTag(repo, fallback)
	ver := strings.TrimPrefix(tag, "v")
	file := strings.ReplaceAll(strings.ReplaceAll(pattern, "{tag}", tag), "{ver}", ver)
	url := "https://github.com/" + repo + "/releases/download/" + tag + "/" + file
	tmp, err := mkTempDir()
	if err != nil {
		return false
	}
	defer os.RemoveAll(tmp)
	if fetch(url, filepath.Join(tmp, file), label+" "+tag) == nil && dryRun {
		return true
	}
	archive := filepath.Join(tmp, file)
	x := filepath.Join(tmp, "x")
	if isFile(archive) && os.MkdirAll(x, 0o755) == nil && extract(archive, x) == nil {
		// Archive sans dossier racine (dxvk-nvapi) : la sonde est à la racine.
		top := x
		if !isFile(filepath.Join(top, probe)) {
			top = topDirOf(x)
		}
		if top != "" && isFile(filepath.Join(top, probe)) {
			if installTree(top, dest) == nil {
				_ = os.WriteFile(filepath.Join(dest, "VERSION"), []byte(tag+"\n"), 0o644)
				printSuccess(label + " " + tag + " installé : " + dest)
				return true
			}
		}
	}
	printError(label + " : installation échouée")
	return false
}

// installedComponent : premier dossier (parmi dirs) qui contient probe.
func installedComponent(probe string, dirs ...string) string {
	for _, d := range dirs {
		if isFile(filepath.Join(d, probe)) {
			return d
		}
	}
	return ""
}

// hasGPU : un fournisseur (0x10de NVIDIA, 0x1002 AMD) dans sysfs DRM.
func hasGPU(vendor string) bool {
	matches, _ := filepath.Glob("/sys/class/drm/card*/device/vendor")
	for _, m := range matches {
		if data, err := os.ReadFile(m); err == nil && strings.Contains(string(data), vendor) {
			return true
		}
	}
	return false
}

// checkNvapi : DXVK-NVAPI (NVAPI, DLSS, couche Reflex), NVIDIA seulement.
func checkNvapi() {
	if !hasGPU("0x10de") {
		return
	}
	section("NVIDIA : DXVK-NVAPI (NVAPI, DLSS, couche Reflex)")
	if nvapi := installedComponent("x64/nvapi64.dll", nvapiDir); nvapi != "" {
		printSuccess("DXVK-NVAPI : " + nvapi)
		return
	}
	fmt.Println("    Carte NVIDIA détectée. NVAPI (DLSS) et la couche Reflex sont optionnelles :")
	fmt.Println("    le lanceur ne les active que sur demande (NVAPI=1, VKREFLEX=1).")
	if !askYesNo("Installer DXVK-NVAPI (~5 Mo) ?", false) {
		printInfo("Ignoré : pas de NVAPI, DLSS ni couche Reflex")
		return
	}
	installDllPack("DXVK-NVAPI", "jp7677/dxvk-nvapi", "v0.9.3",
		"dxvk-nvapi-{tag}.tar.gz", nvapiDir, "x64/nvapi64.dll")
}

// findCachyosFSR4 : première amdxcffx64.dll d'un proton-cachyos installé.
func findCachyosFSR4() string {
	home := os.Getenv("HOME")
	for _, root := range []string{
		filepath.Join(home, ".local/share/Steam/compatibilitytools.d"),
		filepath.Join(home, ".steam/root/compatibilitytools.d"),
		filepath.Join(home, ".config/heroic/tools/proton"),
		"/usr/share/steam/compatibilitytools.d",
	} {
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			continue
		}
		if dll := findIn([]string{root}, 4, func(p string) bool {
			return strings.EqualFold(filepath.Base(p), "amdxcffx64.dll") && strings.Contains(p, "cachyos")
		}); dll != "" {
			return dll
		}
	}
	return ""
}

// checkAMD : RADV (pilote Mesa, fourni par le système) et FSR4 (DLL copiée
// depuis proton-cachyos sur demande, jamais activée par défaut).
func checkAMD() {
	if !hasGPU("0x1002") {
		return
	}
	section("AMD : RADV et FSR4")
	if icds, _ := filepath.Glob("/usr/share/vulkan/icd.d/radeon_icd*.json"); len(icds) > 0 {
		printSuccess("RADV (pilote Vulkan Mesa) : présent")
	} else {
		printError("RADV absent : installer vulkan-radeon (Arch) ou mesa-vulkan-drivers (Debian)")
	}
	for _, dll := range []string{"/usr/share/fsr4/amdxcffx64.dll", filepath.Join(fsr4Dir, "amdxcffx64.dll")} {
		if isFile(dll) {
			printSuccess("FSR4 : " + dll)
			return
		}
	}
	src := findCachyosFSR4()
	if src == "" {
		printInfo("FSR4 : aucun proton-cachyos installé, rien à copier (FSR4 reste désactivé)")
		return
	}
	fmt.Println("    Carte AMD détectée. FSR4 n'est activé que sur demande (FSR4=1 dans autorun.cmd)")
	fmt.Println("    et seulement sur une carte RDNA 4. DLL trouvée : " + src)
	if !askYesNo("Copier amdxcffx64.dll dans "+fsr4Dir+" ?", false) {
		printInfo("Ignoré : FSR4 non copié")
		return
	}
	if dryRun {
		printInfo("Simulation : copie de " + src + " vers " + fsr4Dir)
		return
	}
	dst := filepath.Join(fsr4Dir, "amdxcffx64.dll")
	if os.MkdirAll(fsr4Dir, 0o755) == nil && copyFile(src, dst, 0o644) == nil {
		printSuccess("FSR4 copié : " + dst)
	} else {
		printError("Copie de FSR4 échouée")
	}
}

// checkRendering : DXVK, VKD3D-Proton et d7vk, traduisent Direct3D en Vulkan.
func checkRendering() {
	section("Rendu : DXVK, VKD3D-Proton, d7vk")
	dxvk := installedComponent("x64/dxgi.dll", "/usr/share/dxvk", dxvkDir)
	vkd3d := installedComponent("x64/d3d12.dll", "/usr/share/vkd3d-proton", vkd3dDir)
	d7vk := installedComponent("x32/ddraw.dll", "/usr/share/d7vk", d7vkDir)
	if dxvk != "" {
		printSuccess("DXVK : " + dxvk)
	}
	if vkd3d != "" {
		printSuccess("VKD3D-Proton : " + vkd3d)
	}
	if d7vk != "" {
		printSuccess("d7vk : " + d7vk)
	}
	if dxvk != "" && vkd3d != "" && d7vk != "" {
		return
	}
	fmt.Println("    Direct3D 8-11 (DXVK), 12 (VKD3D-Proton) et DirectDraw (d7vk) traduits")
	fmt.Println("    en Vulkan pour les jeux lancés par Wine ; sans eux, rendu OpenGL de Wine.")
	if !askYesNo("Installer ce qui manque (~25 Mo) ?", true) {
		printInfo("Ignoré : rendu via wined3d (OpenGL) pour les jeux arcade")
		return
	}
	if dxvk == "" {
		installDllPack("DXVK", "doitsujin/dxvk", "v3.1.1", "dxvk-{ver}.tar.gz", dxvkDir, "x64/dxgi.dll")
	}
	if vkd3d == "" {
		_, zstdErr := exec.LookPath("zstd")
		if zstdErr == nil || dryRun {
			installDllPack("VKD3D-Proton", "HansKristian-Work/vkd3d-proton", "v3.0.1",
				"vkd3d-proton-{ver}.tar.zst", vkd3dDir, "x64/d3d12.dll")
		} else {
			printError("VKD3D-Proton : zstd requis pour l'archive (paquet zstd)")
		}
	}
	if d7vk == "" {
		installDllPack("d7vk", "WinterSnowfall/d7vk", "v2.3", "d7vk-{tag}.zip", d7vkDir, "x32/ddraw.dll")
	}
}
