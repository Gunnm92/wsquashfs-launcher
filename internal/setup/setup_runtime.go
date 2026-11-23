package setup

// Runners Wine et Proton, umu, DXVK, VKD3D-Proton, d7vk, NVAPI et FSR4
// (install.sh, sections 5 à 7).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"wsquashfs-launcher/internal/download"
	"wsquashfs-launcher/internal/filetree"
	"wsquashfs-launcher/internal/ui"
)

// ── Runners Wine (section 5) ───────────────────────────────────────────

// installWineArchive : télécharge et installe un runner Wine dans
// wineInstallDir. Vrai si le binaire wine est en place.
func installWineArchive(repo, tag, file, label, name string) bool {
	url := "https://github.com/" + repo + "/releases/download/" + tag + "/" + file
	tmp, err := mkTempDir()
	if err != nil {
		return false
	}
	defer os.RemoveAll(tmp)
	if fetch(url, filepath.Join(tmp, file), label) == nil && dryRun {
		setupDone(label + " : " + doneWord())
		return true
	}
	archive := filepath.Join(tmp, file)
	if isFile(archive) && verifyGithubAsset(archive, repo, tag, file) && os.MkdirAll(filepath.Join(tmp, "x"), 0o755) == nil &&
		extract(archive, filepath.Join(tmp, "x")) == nil {
		top := topDirOf(filepath.Join(tmp, "x"))
		if top != "" {
			if name == "" {
				name = filepath.Base(top)
			}
			dest := filepath.Join(wineInstallDir, name)
			if installTree(top, dest) == nil {
				wine := filetree.Find([]string{dest}, 2, isWineBin(""))
				if isExecutable(wine) {
					setupDone(label + " installé : " + wine)
					return true
				}
			}
		}
	}
	setupFail(label + " : installation échouée")
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
	return installWineArchive("Kron4ek/Wine-Builds", version, file, "wine-tkg "+version+" (~100 Mo)", "")
}

func downloadWineGE() bool {
	tag := githubLatestTag("GloriousEggroll/wine-ge-custom", "GE-Proton8-26")
	file := "wine-lutris-" + tag + "-x86_64.tar.xz"
	return installWineArchive("GloriousEggroll/wine-ge-custom", tag, file, "wine-ge "+tag+" (~234 Mo)", "wine-ge-"+tag)
}

var geRe = regexp.MustCompile(`(?i)ge-|lutris|wine-proton`)

// checkWineRunners : Wine de référence des jeux arcade.
func checkWineRunners() {
	foundTkg := filetree.Find([]string{wineInstallDir, "/opt", "/usr/wine/wine-tkg"}, 3,
		isWineBin("tkg"))
	foundGE := filetree.Find([]string{wineInstallDir, "/usr/wine/ge-custom", "/usr/wine/wine-proton"}, 3,
		func(p string) bool { return isWineBin("")(p) && geRe.MatchString(p) })
	needTkg, needGE := foundTkg == "", foundGE == ""
	if !needTkg {
		setupHave("wine-tkg : " + foundTkg)
	}
	if !needGE {
		setupHave("wine-ge  : " + foundGE)
	}
	if !needTkg && !needGE {
		return
	}
	ui.Note("Les prefixes .wsquashfs Batocera sont conçus pour wine-tkg ou wine-ge ;",
		"le Wine système peut provoquer des incompatibilités de DLL.")
	if needTkg {
		setupTodo("wine-tkg manquant", "Kron4ek staging, ~100 Mo — runner par défaut")
	}
	if needGE {
		setupTodo("wine-ge manquant", "GloriousEggroll, ~234 Mo — alternative")
	}
	ui.Menu([]string{"Installer ce qui manque", "wine-tkg seulement", "wine-ge seulement", "Ignorer"}, "2")
	switch askChoice("Choix", "[1-4] (défaut : 2)", "2") {
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
		setupSkip("Ignoré : le Wine système sera utilisé (certaines DLL peuvent manquer)")
	}
}

// ── Proton et umu (section 6) ──────────────────────────────────────────

// findProton : Proton préféré du lanceur (proton-cachyos, puis UMU-Proton).
func findProton() string {
	if d := findProtonMatching("proton-cachyos*"); d != "" {
		return d
	}
	return findProtonMatching("UMU-Proton*")
}

// findProtonMatching : premier Proton complet (proton et files/bin/wine) dont
// le dossier correspond au motif, dans les dossiers de Proton connus.
func findProtonMatching(pattern string) string {
	home := os.Getenv("HOME")
	for _, base := range []string{
		protonInstallDir,
		filepath.Join(home, ".steam/root/compatibilitytools.d"),
		filepath.Join(home, ".config/heroic/tools/proton"),
		"/usr/share/steam/compatibilitytools.d",
	} {
		dirs, _ := filepath.Glob(filepath.Join(base, pattern))
		for _, d := range dirs {
			if isExecutable(filepath.Join(d, "proton")) && isExecutable(filepath.Join(d, "files/bin/wine")) {
				return d
			}
		}
	}
	return ""
}

// downloadUmu : umu-run en zipapp, dans umuInstallDir.
func downloadUmu() bool {
	if _, err := exec.LookPath("python3"); err != nil {
		setupFail("python3 requis pour umu-run")
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
		setupDone("umu-launcher " + tag + " : " + doneWord())
		return true
	}
	archive := filepath.Join(tmp, file)
	if isFile(archive) && verifyGithubAsset(archive, "Open-Wine-Components/umu-launcher", tag, file) &&
		runCmd("tar", "-xf", archive, "-C", tmp) == nil && isFile(filepath.Join(tmp, "umu/umu-run")) {
		os.RemoveAll(umuInstallDir)
		link := filepath.Join(installDir, "umu-run")
		target := filepath.Join(umuInstallDir, "umu-run")
		if os.MkdirAll(umuInstallDir, 0o755) == nil && os.MkdirAll(installDir, 0o755) == nil &&
			filetree.CopyFile(filepath.Join(tmp, "umu/umu-run"), target, 0o755) == nil {
			os.Remove(link)
			if os.Symlink(target, link) == nil {
				setupDone("umu-run " + tag + " installé : " + link)
				return true
			}
		}
	}
	setupFail("umu-launcher : installation échouée")
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
	return downloadProtonRelease("Open-Wine-Components/umu-proton", "UMU-Proton-10.0-4", "~470 Mo")
}

// downloadGEProton : dernière version de GE-Proton (SHA-512 vérifiée), dans
// protonInstallDir (18/11 : plus de dépendance au GE-Proton de Heroic).
// Choisie par PROTON=GE-Proton ; les versions précédentes sont gardées.
func downloadGEProton() bool {
	return downloadProtonRelease("GloriousEggroll/proton-ge-custom", "GE-Proton11-5", "~500 Mo")
}

// downloadProtonCachyos : dernière version de proton-cachyos (variante
// « slr », release GitHub de CachyOS, SHA-512 vérifiée), dans
// protonInstallDir (20/11 : plus seulement le paquet pacman, qu'une image
// Docker perd à chaque mise à jour). Archive optimisée x86-64-v3 si le
// processeur la permet. Proton par défaut du lanceur ; les versions
// précédentes sont gardées (--clean les retire).
func downloadProtonCachyos() bool {
	repo := "CachyOS/proton-cachyos"
	tag := githubLatestTag(repo, "cachyos-11.0-20261015-slr")
	if protonInstalled(tag) {
		return true
	}
	name := "proton-" + tag + "-" + cachyosArch()
	return installProtonArchive(repo, tag, name+".tar.xz", name+".sha512sum", "proton-"+tag, "~330 Mo")
}

// cachyosArch : suffixe d'architecture des archives proton-cachyos.
func cachyosArch() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	if cpuinfo, err := os.ReadFile("/proc/cpuinfo"); err == nil && cpuHasX86_64V3(string(cpuinfo)) {
		return "x86_64_v3"
	}
	return "x86_64"
}

// cpuHasX86_64V3 : le processeur (drapeaux de /proc/cpuinfo) a toutes les
// instructions du niveau x86-64-v3 (AVX2 et compagnie).
func cpuHasX86_64V3(cpuinfo string) bool {
	for _, line := range strings.Split(cpuinfo, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "flags" {
			continue
		}
		flags := map[string]bool{}
		for _, f := range strings.Fields(val) {
			flags[f] = true
		}
		for _, need := range []string{"avx", "avx2", "bmi1", "bmi2", "f16c", "fma", "abm", "movbe", "xsave"} {
			if !flags[need] {
				return false
			}
		}
		return true
	}
	return false
}

// protonInstalled : cette version est déjà dans protonInstallDir (dossier
// nommé d'après elle, avec ou sans suffixe d'architecture : reconnu aussi par
// le fichier version).
func protonInstalled(tag string) bool {
	dirs, _ := filepath.Glob(filepath.Join(protonInstallDir, "*"))
	for _, d := range dirs {
		if isExecutable(filepath.Join(d, "proton")) && protonRunnerIs(d, tag) {
			setupHave(tag + " : " + d)
			return true
		}
	}
	return false
}

// downloadProtonRelease : Proton d'une release GitHub, archive et somme
// SHA-512 publiées côte à côte : <tag>-x86_64.tar.gz (GE-Proton depuis 11-x,
// UMU-Proton depuis 11.0-2e) ou <tag>.tar.gz (UMU-Proton avant), dans
// protonInstallDir. Déjà installé : rien n'est téléchargé.
func downloadProtonRelease(repo, fallbackTag, size string) bool {
	tag := githubLatestTag(repo, fallbackTag)
	if protonInstalled(tag) {
		return true
	}
	base := "https://github.com/" + repo + "/releases/download/" + tag
	file := tag + ".tar.gz"
	if download.Exists(base + "/" + tag + "-x86_64.tar.gz") {
		file = tag + "-x86_64.tar.gz"
	}
	return installProtonArchive(repo, tag, file, strings.TrimSuffix(file, ".tar.gz")+".sha512sum", tag, size)
}

// installProtonArchive : télécharge l'archive file d'une release et sa somme
// SHA-512 sum, la vérifie et installe son dossier racine dans protonInstallDir.
func installProtonArchive(repo, tag, file, sum, label, size string) bool {
	base := "https://github.com/" + repo + "/releases/download/" + tag
	tmp, err := mkTempDir()
	if err != nil {
		return false
	}
	defer os.RemoveAll(tmp)
	if fetch(base+"/"+file, filepath.Join(tmp, file), label+" ("+size+")") == nil && dryRun {
		setupDone(label + " : " + doneWord())
		return true
	}
	if !isFile(filepath.Join(tmp, file)) || download.File(base+"/"+sum, filepath.Join(tmp, sum)) != nil {
		setupFail(label + " : téléchargement échoué")
		return false
	}
	check := exec.Command("sha512sum", "-c", "--quiet", sum)
	check.Dir = tmp
	if check.Run() != nil {
		setupErr("Somme de contrôle SHA-512 incorrecte, archive ignorée")
		return false
	}
	x := filepath.Join(tmp, "x")
	if os.MkdirAll(x, 0o755) == nil && extract(filepath.Join(tmp, file), x) == nil {
		top := topDirOf(x)
		if top != "" && isExecutable(filepath.Join(top, "proton")) {
			dest := filepath.Join(protonInstallDir, filepath.Base(top))
			if installTree(top, dest) == nil {
				setupDone(label + " installé : " + dest)
				return true
			}
		}
	}
	setupFail(label + " : extraction échouée (archive corrompue ?)")
	return false
}

// installProton : proton-cachyos depuis GitHub (toutes distributions, dans
// le dossier de l'utilisateur : rien dans le système, rien de perdu à la mise
// à jour d'une image Docker), sinon UMU-Proton.
func installProton() {
	if !downloadProtonCachyos() {
		downloadUmuProton()
	}
}

func checkProton() {
	umu, _ := exec.LookPath("umu-run")
	// proton-cachyos est le Proton par défaut : un UMU-Proton seul ne suffit
	// pas, proton-cachyos est proposé quand même.
	proton := findProtonMatching("proton-cachyos*")
	if umu != "" {
		setupHave("umu-run : " + umu)
	}
	if proton != "" {
		setupHave("Proton  : " + proton)
	} else if other := findProtonMatching("UMU-Proton*"); other != "" {
		setupHave("Proton  : " + other + " (en attendant proton-cachyos)")
	}
	if umu != "" && proton != "" {
		return
	}
	ui.Note("Les .wsquashfs sans prefix (jeux PC emballés seuls) et les prefixes",
		"créés par Proton (Heroic, Steam) se lancent via umu et Proton.",
		"Inutile pour les seuls jeux arcade Batocera (wine-tkg).")
	if umu == "" {
		setupTodo("umu-run manquant")
	}
	if proton == "" {
		setupTodo("proton-cachyos manquant", "Proton par défaut, ~330 Mo, sinon UMU-Proton")
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
	setupSkip("Sans umu, ces jeux seront lancés par le Wine de Proton seul (écran noir possible)")
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
		setupDone(label + " " + tag + " : " + doneWord())
		return true
	}
	archive := filepath.Join(tmp, file)
	x := filepath.Join(tmp, "x")
	if isFile(archive) && verifyGithubAsset(archive, repo, tag, file) && os.MkdirAll(x, 0o755) == nil && extract(archive, x) == nil {
		// Archive sans dossier racine (dxvk-nvapi) : la sonde est à la racine.
		top := x
		if !isFile(filepath.Join(top, probe)) {
			top = topDirOf(x)
		}
		if top != "" && isFile(filepath.Join(top, probe)) {
			if installTree(top, dest) == nil {
				_ = os.WriteFile(filepath.Join(dest, "VERSION"), []byte(tag+"\n"), 0o644)
				setupDone(label + " " + tag + " installé : " + dest)
				return true
			}
		}
	}
	setupFail(label + " : installation échouée")
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
	if nvapi := installedComponent("x64/nvapi64.dll", nvapiDir); nvapi != "" {
		setupHave("DXVK-NVAPI : " + nvapi)
		return
	}
	ui.Note("Carte NVIDIA détectée. NVAPI (DLSS) et la couche Reflex sont optionnelles :",
		"le lanceur ne les active que sur demande (NVAPI=1, VKREFLEX=1).")
	setupTodo("DXVK-NVAPI manquant")
	if !askYesNo("Installer DXVK-NVAPI (~5 Mo) ?", false) {
		setupSkip("Ignoré : pas de NVAPI, DLSS ni couche Reflex")
		return
	}
	installDllPack("DXVK-NVAPI", "jp7677/dxvk-nvapi", "v0.9.3",
		"dxvk-nvapi-{tag}.tar.gz", nvapiDir, "x64/nvapi64.dll")
}

// optiscalerAssetRe : archive d'OptiScaler (nom daté : Optiscaler_0.9.5-final.20261116.7z).
var optiscalerAssetRe = regexp.MustCompile(`(?i)^optiscaler_.*\.7z$`)

// downloadOptiscaler : dernière version d'OptiScaler (release GitHub, SHA-256
// publiée par GitHub vérifiée) dans optiscalerDir ; déjà à jour : rien.
func downloadOptiscaler() bool {
	repo := "optiscaler/OptiScaler"
	tag := githubLatestTag(repo, "v0.9.5")
	if data, err := os.ReadFile(filepath.Join(optiscalerDir, "VERSION")); err == nil &&
		strings.TrimSpace(string(data)) == tag && isFile(filepath.Join(optiscalerDir, "OptiScaler.dll")) {
		setupHave("OptiScaler " + tag + " : " + optiscalerDir)
		return true
	}
	if sevenZip() == "" && !cmdOK("bsdtar", "--version") && !dryRun {
		setupFail("OptiScaler : 7-Zip requis pour l'archive .7z (paquet 7zip)")
		return false
	}
	file := githubAssetMatching(repo, tag, optiscalerAssetRe)
	if file == "" {
		setupFail("OptiScaler " + tag + " : archive introuvable dans la version publiée")
		return false
	}
	return installDllPack("OptiScaler", repo, tag, file, optiscalerDir, "OptiScaler.dll")
}

// checkOptiscaler : OptiScaler (upscaler et génération d'images dans le jeu),
// installé sur demande, activé jeu par jeu (OPTISCALER=1, jamais par défaut).
func checkOptiscaler() {
	if isFile(filepath.Join(optiscalerDir, "OptiScaler.dll")) {
		v, _ := os.ReadFile(filepath.Join(optiscalerDir, "VERSION"))
		setupHave(strings.TrimSpace("OptiScaler "+strings.TrimSpace(string(v)))+" : "+optiscalerDir,
			"mise à jour : --setup optiscaler")
		return
	}
	ui.Note("OptiScaler remplace l'upscaler d'un jeu (DLSS, FSR, XeSS) et ajoute la",
		"génération d'images ; le lanceur ne l'active que sur demande (OPTISCALER=1).")
	setupTodo("OptiScaler manquant")
	if !askYesNo("Installer OptiScaler (~300 Mo) ?", false) {
		setupSkip("Ignoré : OPTISCALER sans effet")
		return
	}
	downloadOptiscaler()
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
		if dll := filetree.Find([]string{root}, 4, func(p string) bool {
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
	if icds, _ := filepath.Glob("/usr/share/vulkan/icd.d/radeon_icd*.json"); len(icds) > 0 {
		setupHave("RADV (pilote Vulkan Mesa) : présent")
	} else {
		setupFail("RADV absent : installer vulkan-radeon (Arch) ou mesa-vulkan-drivers (Debian)")
	}
	for _, dll := range []string{"/usr/share/fsr4/amdxcffx64.dll", filepath.Join(fsr4Dir, "amdxcffx64.dll")} {
		if isFile(dll) {
			setupHave("FSR4 : " + dll)
			return
		}
	}
	src := findCachyosFSR4()
	if src == "" {
		setupSkip("FSR4 : aucun proton-cachyos installé, rien à copier (FSR4 reste désactivé)")
		return
	}
	ui.Note("Carte AMD détectée. FSR4 n'est activé que sur demande (FSR4=1 dans autorun.cmd)",
		"et seulement sur une carte RDNA 4. DLL trouvée : "+src)
	if !askYesNo("Copier amdxcffx64.dll dans "+fsr4Dir+" ?", false) {
		setupSkip("Ignoré : FSR4 non copié")
		return
	}
	if dryRun {
		simLine("copie de " + src + " vers " + fsr4Dir)
		return
	}
	dst := filepath.Join(fsr4Dir, "amdxcffx64.dll")
	if os.MkdirAll(fsr4Dir, 0o755) == nil && filetree.CopyFile(src, dst, 0o644) == nil {
		setupDone("FSR4 copié : " + dst)
	} else {
		setupFail("Copie de FSR4 échouée")
	}
}

// checkRendering : DXVK, VKD3D-Proton et d7vk, traduisent Direct3D en Vulkan.
func checkRendering() {
	dxvk := installedComponent("x64/dxgi.dll", "/usr/share/dxvk", dxvkDir)
	vkd3d := installedComponent("x64/d3d12.dll", "/usr/share/vkd3d-proton", vkd3dDir)
	d7vk := installedComponent("x32/ddraw.dll", "/usr/share/d7vk", d7vkDir)
	if dxvk != "" {
		setupHave("DXVK : " + dxvk)
	} else {
		setupTodo("DXVK manquant")
	}
	if vkd3d != "" {
		setupHave("VKD3D-Proton : " + vkd3d)
	} else {
		setupTodo("VKD3D-Proton manquant")
	}
	if d7vk != "" {
		setupHave("d7vk : " + d7vk)
	} else {
		setupTodo("d7vk manquant")
	}
	if dxvk != "" && vkd3d != "" && d7vk != "" {
		return
	}
	ui.Note("Direct3D 8-11 (DXVK), 12 (VKD3D-Proton) et DirectDraw (d7vk) traduits",
		"en Vulkan pour les jeux lancés par Wine ; sans eux, rendu OpenGL de Wine.")
	if !askYesNo("Installer ce qui manque (~25 Mo) ?", true) {
		setupSkip("Ignoré : rendu via wined3d (OpenGL) pour les jeux arcade")
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
			setupFail("VKD3D-Proton : zstd requis pour l'archive (paquet zstd)")
		}
	}
	if d7vk == "" {
		installDllPack("d7vk", "WinterSnowfall/d7vk", "v2.3", "d7vk-{tag}.zip", d7vkDir, "x32/ddraw.dll")
	}
}

// protonRunnerIs : le dossier correspond à cette version (nom ou fichier version).
func protonRunnerIs(d, want string) bool {
	if filepath.Base(d) == want {
		return true
	}
	data, err := os.ReadFile(d + "/version")
	if err != nil {
		return false
	}
	fields := strings.Fields(strings.SplitN(string(data), "\n", 2)[0])
	return len(fields) > 0 && fields[len(fields)-1] == want
}
