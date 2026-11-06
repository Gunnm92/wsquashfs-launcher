package main

// Distribution et paquets système (install.sh, sections 4 et 1 pour
// detect_distro) : apt pour Debian et dérivés, pacman pour Arch et dérivés.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// readOSRelease : fichier os-release (KEY=VALUE, guillemets retirés).
func readOSRelease(path string) map[string]string {
	vars := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return vars
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		v = strings.Trim(v, `"'`)
		vars[k] = v
	}
	return vars
}

// detectDistro : renseigne distro et distroName.
func detectDistro() {
	path := os.Getenv("OS_RELEASE")
	if path == "" {
		path = "/etc/os-release"
	}
	vars := readOSRelease(path)
	id, like := vars["ID"], vars["ID_LIKE"]
	distroName = vars["PRETTY_NAME"]
	if distroName == "" {
		distroName = id
	}
	fields := " " + id + " " + like + " "
	switch {
	case strings.Contains(fields, " debian ") || strings.Contains(fields, " ubuntu "):
		distro = "debian"
	case strings.Contains(fields, " arch ") || strings.Contains(fields, " cachyos "):
		distro = "arch"
	default:
		if _, err := exec.LookPath("pacman"); err == nil {
			distro = "arch"
		} else if _, err := exec.LookPath("apt-get"); err == nil {
			distro = "debian"
		} else {
			distro = "autre"
		}
	}
	if distroName == "" {
		distroName = "inconnue"
	}
}

// needSudo : vrai si les commandes système peuvent être lancées.
func needSudo() bool {
	if len(sudoCmd) == 0 {
		return true
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		printError("sudo absent — relancez en root : sudo " + exePath() + " --setup")
		return false
	}
	printInfo("Non-root : sudo utilisé pour les paquets système")
	return true
}

func checkDependencies() bool {
	section("Paquets système (" + distroName + ")")
	if !needSudo() {
		return false
	}
	switch distro {
	case "debian":
		return depsDebian()
	case "arch":
		return depsArch()
	default:
		return depsOther()
	}
}

// ── Debian / Ubuntu (apt) ──────────────────────────────────────────────

func dpkgInstalled(pkg string) bool {
	out, err := exec.Command("dpkg", "-l", pkg).Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "ii") {
			return true
		}
	}
	return false
}

// aptEnsure : installe les paquets qui manquent.
func aptEnsure(label string, pkgs ...string) error {
	var missing []string
	for _, p := range pkgs {
		if !dpkgInstalled(p) {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		printSuccess(label + " : déjà installé (" + strings.Join(pkgs, " ") + ")")
		return nil
	}
	printInfo(label + " : installation de " + strings.Join(missing, " ") + "...")
	if err := runRoot(append([]string{"apt-get", "install", "-y"}, missing...)...); err != nil {
		printError(label + " : échec de l'installation de " + strings.Join(missing, " "))
		return err
	}
	printSuccess(label + " : " + strings.Join(missing, " ") + " " + doneWord())
	return nil
}

func depsDebian() bool {
	ok := true
	// wine32:i386 et libxkbcommon0:i386 : bibliothèques 32 bits de wine-tkg
	// (jeux 32 bits, « no driver could be loaded » sinon, Virtua Tennis 4).
	if !dpkgInstalled("wine32:i386") {
		if runRoot("dpkg", "--add-architecture", "i386") != nil || runRoot("apt-get", "update") != nil {
			ok = false
		}
	}
	if aptEnsure("Wine 32 bits", "wine32:i386", "libxkbcommon0:i386") != nil {
		ok = false
	}
	if aptEnsure("Images squashfs", "squashfuse", "squashfs-tools", "xz-utils") != nil {
		ok = false
	}
	if aptEnsure("Mode overlay", "fuse-overlayfs") != nil {
		printInfo("fuse-overlayfs indisponible — mode copy utilisé (fonctionnel)")
	}
	// getfattr : --clean ; zstd : VKD3D-Proton ; unzip : d7vk
	_ = aptEnsure("Outils (--clean, archives)", "attr", "zstd", "unzip")
	// MangoHud (optionnel : mangohud wsquashfs-launcher jeu.wsquashfs)
	if aptEnsure("MangoHud (facultatif)", "mangohud", "mangohud:i386") != nil &&
		aptEnsure("MangoHud (facultatif)", "mangohud") != nil {
		printInfo("mangohud indisponible — overlay MangoHud non disponible")
	}
	return ok
}

// ── Arch, CachyOS, Manjaro… (pacman) ───────────────────────────────────

func pacmanInstalled(p string) bool { return cmdOK("pacman", "-Q", p) }
func pacmanAvailable(p string) bool { return cmdOK("pacman", "-Si", p) }

// pacmanSynced : bases synchronisées (sans elles, pacman -Si ne trouve rien).
func pacmanSynced() bool { return cmdOK("pacman", "-Si", "pacman") }

func multilibEnabled() bool {
	if _, err := exec.LookPath("pacman-conf"); err == nil {
		out, err := exec.Command("pacman-conf", "--repo-list").Output()
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(line) == "multilib" {
				return true
			}
		}
		return false
	}
	data, err := os.ReadFile("/etc/pacman.conf")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "[multilib]") {
			return true
		}
	}
	return false
}

// pacmanEnsure : installe ceux qui manquent et existent dans les dépôts ; les
// autres sont signalés.
func pacmanEnsure(label string, pkgs ...string) error {
	var missing, unknown []string
	for _, p := range pkgs {
		if pacmanInstalled(p) {
			continue
		}
		if pacmanAvailable(p) {
			missing = append(missing, p)
		} else {
			unknown = append(unknown, p)
		}
	}
	if len(unknown) > 0 {
		printInfo(label + " : absents des dépôts, ignorés : " + strings.Join(unknown, " "))
	}
	if len(missing) == 0 {
		if len(unknown) == 0 {
			printSuccess(label + " : déjà installé")
		}
		return nil
	}
	printInfo(label + " : installation de " + strings.Join(missing, " ") + "...")
	args := []string{"pacman", "-S", "--needed"}
	if assumeYes || !hasTTY() {
		args = append(args, "--noconfirm")
	}
	if err := runRoot(append(args, missing...)...); err != nil {
		printError(label + " : échec de l'installation de " + strings.Join(missing, " "))
		return err
	}
	printSuccess(label + " : " + strings.Join(missing, " ") + " " + doneWord())
	return nil
}

// archLib32Packages : bibliothèques 32 bits (multilib) de wine-tkg, et la
// version 32 bits des pilotes Vulkan/OpenGL installés.
func archLib32Packages() []string {
	pkgs := []string{
		"lib32-glibc", "lib32-gcc-libs",
		"lib32-libx11", "lib32-libxext", "lib32-libxrender", "lib32-libxrandr", "lib32-libxi",
		"lib32-libxcursor", "lib32-libxcomposite", "lib32-libxinerama", "lib32-libxfixes",
		"lib32-libxkbcommon", "lib32-wayland",
		"lib32-freetype2", "lib32-fontconfig", "lib32-gnutls",
		"lib32-libpulse", "lib32-alsa-lib", "lib32-alsa-plugins",
		"lib32-mesa", "lib32-vulkan-icd-loader", "lib32-sdl2",
		"lib32-gst-plugins-base-libs",
	}
	for _, driver := range []string{"nvidia-utils", "vulkan-radeon", "vulkan-intel"} {
		if pacmanInstalled(driver) {
			pkgs = append(pkgs, "lib32-"+driver)
		}
	}
	return pkgs
}

func depsArch() bool {
	if !pacmanSynced() {
		printError("Bases pacman non synchronisées : lancez d'abord « sudo pacman -Syu »")
		return false
	}
	ok := true
	if pacmanEnsure("Images squashfs", "squashfuse", "squashfs-tools", "xz") != nil {
		ok = false
	}
	if pacmanEnsure("Mode overlay", "fuse-overlayfs") != nil {
		printInfo("fuse-overlayfs indisponible — mode copy utilisé (fonctionnel)")
	}
	_ = pacmanEnsure("Outils (--clean, archives)", "attr", "zstd", "unzip")
	if multilibEnabled() {
		if pacmanEnsure("Bibliothèques 32 bits (wine-tkg, jeux 32 bits)", archLib32Packages()...) != nil {
			ok = false
		}
		_ = pacmanEnsure("MangoHud (facultatif)", "mangohud", "lib32-mangohud")
	} else {
		printError("Dépôt [multilib] désactivé : les jeux 32 bits (arcade Batocera) ne se lanceront pas.")
		fmt.Println("    Activez-le dans /etc/pacman.conf (section [multilib]), puis relancez l'installation.")
		ok = false
		_ = pacmanEnsure("MangoHud (facultatif)", "mangohud")
	}
	return ok
}

func depsOther() bool {
	printError("Distribution non prise en charge pour les paquets système (" + distroName + ").")
	fmt.Println("    Installez à la main : squashfuse, squashfs-tools, fuse-overlayfs, attr,")
	fmt.Println("    zstd, unzip, et les bibliothèques 32 bits de Wine (jeux arcade 32 bits).")
	return askYesNo("Continuer (runners, DXVK, lanceur) ?", false)
}

// exePath : chemin de ce binaire, pour les messages.
func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return "wsquashfs-launcher"
	}
	return p
}
