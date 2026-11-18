package main

// Distribution et paquets système (install.sh, sections 4 et 1 pour
// detect_distro) : apt pour Debian et dérivés, pacman pour Arch et dérivés.

import (
	"bufio"
	"os"
	"os/exec"
	"strings"

	"wsquashfs-launcher/internal/ui"
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
	case id == "steamos":
		distro = "steamos"
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
		setupFail("sudo absent — relancez en root : sudo " + exePath() + " --setup")
		return false
	}
	ui.Item(ui.Info, "Non-root : sudo utilisé pour les paquets système")
	return true
}

func checkDependencies() bool {
	if distro == "steamos" {
		return depsSteamOS() // vérification seulement : ni sudo ni pacman
	}
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
		setupHave(label + " : déjà installé (" + strings.Join(pkgs, " ") + ")")
		return nil
	}
	ui.Item(ui.Info, label+" : installation de "+strings.Join(missing, " ")+"...")
	if err := runRoot(append([]string{"apt-get", "install", "-y"}, missing...)...); err != nil {
		setupFail(label + " : échec de l'installation de " + strings.Join(missing, " "))
		return err
	}
	setupDone(label + " : " + strings.Join(missing, " ") + " " + doneWord())
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
	if aptEnsure("Images squashfs", "fuse3", "squashfs-tools", "xz-utils") != nil {
		ok = false
	}
	if aptEnsure("Mode overlay", "fuse-overlayfs") != nil {
		setupSkip("fuse-overlayfs indisponible — mode copy utilisé (fonctionnel)")
	}
	// getfattr : --clean ; zstd : VKD3D-Proton ; unzip : d7vk
	_ = aptEnsure("Outils (--clean, archives)", "attr", "zstd", "unzip")
	// Presse-papiers de l'icône (--tray : code d'association copié), Wayland et X11
	if aptEnsure("Presse-papiers (--tray, facultatif)", "wl-clipboard", "xclip") != nil {
		setupSkip("wl-clipboard/xclip indisponibles — le code d'association de --tray reste à recopier")
	}
	// MangoHud (optionnel : mangohud wsquashfs-launcher jeu.wsquashfs)
	if aptEnsure("MangoHud (facultatif)", "mangohud", "mangohud:i386") != nil &&
		aptEnsure("MangoHud (facultatif)", "mangohud") != nil {
		setupSkip("mangohud indisponible — overlay MangoHud non disponible")
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
		setupSkip(label + " : absents des dépôts, ignorés : " + strings.Join(unknown, " "))
	}
	if len(missing) == 0 {
		if len(unknown) == 0 {
			setupHave(label + " : déjà installé")
		}
		return nil
	}
	ui.Item(ui.Info, label+" : installation de "+strings.Join(missing, " ")+"...")
	args := []string{"pacman", "-S", "--needed"}
	if assumeYes || !hasTTY() {
		args = append(args, "--noconfirm")
	}
	if err := runRoot(append(args, missing...)...); err != nil {
		setupFail(label + " : échec de l'installation de " + strings.Join(missing, " "))
		return err
	}
	setupDone(label + " : " + strings.Join(missing, " ") + " " + doneWord())
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
		setupFail("Bases pacman non synchronisées : lancez d'abord « sudo pacman -Syu »")
		return false
	}
	ok := true
	if pacmanEnsure("Images squashfs", "fuse3", "squashfs-tools", "xz") != nil {
		ok = false
	}
	if pacmanEnsure("Mode overlay", "fuse-overlayfs") != nil {
		setupSkip("fuse-overlayfs indisponible — mode copy utilisé (fonctionnel)")
	}
	_ = pacmanEnsure("Outils (--clean, archives)", "attr", "zstd", "unzip")
	_ = pacmanEnsure("Presse-papiers (--tray, facultatif)", "wl-clipboard", "xclip")
	if multilibEnabled() {
		if pacmanEnsure("Bibliothèques 32 bits (wine-tkg, jeux 32 bits)", archLib32Packages()...) != nil {
			ok = false
		}
		_ = pacmanEnsure("MangoHud (facultatif)", "mangohud", "lib32-mangohud")
	} else {
		setupFail("Dépôt [multilib] désactivé : les jeux 32 bits (arcade Batocera) ne se lanceront pas.")
		ui.Note("Activez-le dans /etc/pacman.conf (section [multilib]), puis relancez l'installation.")
		ok = false
		_ = pacmanEnsure("MangoHud (facultatif)", "mangohud")
	}
	return ok
}

// ── SteamOS (Steam Deck) ───────────────────────────────────────────────

// depsSteamOS : la racine est en lecture seule et remplacée à chaque mise à
// jour du système : rien n'est installé par pacman (ce serait effacé). Tout
// ce que le lanceur demande y est déjà, bibliothèques 32 bits comprises
// (Steam) : SteamOS 3.9, vérifié le 16/11. On vérifie seulement.
func depsSteamOS() bool {
	ui.Item(ui.Info, "SteamOS : système en lecture seule, aucun paquet installé (vérification seulement)")
	ok := true
	check := func(label string, required bool, pkgs ...string) {
		var missing []string
		for _, p := range pkgs {
			if !pacmanInstalled(p) {
				missing = append(missing, p)
			}
		}
		switch {
		case len(missing) == 0:
			setupHave(label + " : présent")
		case required:
			setupFail(label + " : absent (" + strings.Join(missing, " ") + ")")
			ok = false
		default:
			setupSkip(label + " : absent (" + strings.Join(missing, " ") + ")")
		}
	}
	check("Images squashfs", true, "fuse3", "squashfs-tools", "xz")
	check("Mode overlay", false, "fuse-overlayfs")
	check("Outils (--clean, archives)", false, "attr", "zstd", "unzip")
	check("Bibliothèques 32 bits (wine-tkg, jeux 32 bits)", true,
		"lib32-glibc", "lib32-gcc-libs", "lib32-libx11", "lib32-wayland", "lib32-freetype2",
		"lib32-fontconfig", "lib32-gnutls", "lib32-libpulse", "lib32-alsa-lib", "lib32-mesa",
		"lib32-vulkan-icd-loader", "lib32-vulkan-radeon")
	// Absentes de SteamOS 3.9 : pilote Wayland 32 bits de Wine (le Deck passe
	// par XWayland/gamescope), SDL2 et GStreamer 32 bits (rares jeux 32 bits).
	check("Bibliothèques 32 bits facultatives", false, "lib32-libxkbcommon", "lib32-sdl2", "lib32-gst-plugins-base-libs")
	if !pacmanInstalled("wl-clipboard") && !pacmanInstalled("xclip") {
		setupSkip("Presse-papiers (--tray) : ni wl-clipboard ni xclip — le code d'association de --tray reste à recopier")
	} else {
		setupHave("Presse-papiers (--tray) : présent")
	}
	check("MangoHud (facultatif)", false, "mangohud", "lib32-mangohud")
	if !ok {
		ui.Note("SteamOS : un paquet système manquant ne s'installe pas durablement (racine en lecture seule,",
			"remplacée aux mises à jour) ; le lanceur passe au mode copy si FUSE manque.")
	}
	return ok
}

func depsOther() bool {
	setupFail("Distribution non prise en charge pour les paquets système (" + distroName + ").")
	ui.Note("Installez à la main : fuse3, squashfs-tools, fuse-overlayfs, attr,",
		"zstd, unzip, et les bibliothèques 32 bits de Wine (jeux arcade 32 bits) ;",
		"pour --tray : wl-clipboard ou xclip.")
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
