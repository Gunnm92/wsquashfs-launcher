package main

// Installeur intégré (--setup) : porte install.sh. Sections 8 (lanceur, MIME,
// bureau, PATH), 8b (agent pour wsquashfs-manager), 9 (désinstallation) et
// 10 (enchaînement).

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// Emplacements (install.sh, section 1).
var (
	wsqData          string
	wineInstallDir   string
	umuInstallDir    string
	dxvkDir          string
	vkd3dDir         string
	d7vkDir          string
	nvapiDir         string
	fsr4Dir          string
	protonInstallDir string
	desktopFile      string
	agentPort        = 8790
	agentUnitDir     string
)

// setupInit : calcule les emplacements à partir de HOME et XDG_CONFIG_HOME.
func setupInit() {
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	wsqData = filepath.Join(home, ".local/share/wsquashfs")
	wineInstallDir = filepath.Join(wsqData, "wine")
	umuInstallDir = filepath.Join(wsqData, "umu")
	dxvkDir = filepath.Join(wsqData, "dxvk")
	vkd3dDir = filepath.Join(wsqData, "vkd3d-proton")
	d7vkDir = filepath.Join(wsqData, "d7vk")
	nvapiDir = filepath.Join(wsqData, "dxvk-nvapi")
	fsr4Dir = filepath.Join(wsqData, "fsr4")
	protonInstallDir = filepath.Join(home, ".local/share/Steam/compatibilitytools.d")
	desktopFile = filepath.Join(home, ".local/share/applications/wsquashfs-launcher.desktop")
	agentUnitDir = filepath.Join(configHome, "systemd/user")
}

// checkRoot : dossier d'installation et préfixe sudo selon l'utilisateur.
func checkRoot() {
	if os.Geteuid() == 0 {
		installDir = "/usr/local/bin"
		sudoCmd = nil
		return
	}
	installDir = filepath.Join(os.Getenv("HOME"), ".local/bin")
	sudoCmd = []string{"sudo"}
}

// installRepoFile : télécharge un fichier du dépôt dans dest, via un fichier
// voisin renommé ensuite : un lanceur en cours d'exécution garde son fichier.
func installRepoFile(rel, dest string) error {
	if dryRun {
		fmt.Println("    [simulation] " + rel + " → " + dest)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.new.%d", dest, os.Getpid())
	if err := downloadTo("https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main/"+rel, tmp); err != nil {
		os.Remove(tmp)
		printError("Téléchargement de " + rel + " impossible")
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// installSelf : le lanceur installé est ce binaire lui-même.
func installSelf(dest string) error {
	if dryRun {
		fmt.Println("    [simulation] " + exePath() + " → " + dest)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.new.%d", dest, os.Getpid())
	if err := copyFile(exePath(), tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// installLauncher : le lanceur du dépôt (script bash, comme install.sh), puis
// le pont DualSense. Le binaire Go ne lance pas encore les jeux : il ne
// remplacera ce script qu'à la parité (installSelf est prêt pour ce moment).
func installLauncher() bool {
	section("Lanceur")
	dest := filepath.Join(installDir, "wsquashfs-launcher")
	if err := installRepoFile("wsquashfs-launcher", dest); err != nil {
		printError("Installation du lanceur impossible : " + err.Error())
		return false
	}
	printSuccess("Lanceur : " + dest)
	bridge := filepath.Join(installDir, "dualsense-usb-bridge")
	if installRepoFile("tools/dualsense-usb-bridge/dualsense-usb-bridge", bridge) == nil && !dryRun {
		printSuccess("Pont DualSense : " + bridge)
	}
	return true
}

const mimeXML = `<?xml version="1.0" encoding="UTF-8"?>
<mime-info xmlns="http://www.freedesktop.org/standards/shared-mime-info">
    <mime-type type="application/x-wsquashfs">
        <comment>WSquashFS Archive (Batocera)</comment>
        <glob pattern="*.wsquashfs"/>
        <magic priority="50">
            <match type="string" offset="0" value="hsqs"/>
        </magic>
    </mime-type>
</mime-info>
`

// createMimeType : association des .wsquashfs au lanceur (double-clic).
func createMimeType() {
	mimeDir := filepath.Join(os.Getenv("HOME"), ".local/share/mime")
	if !askYesNo("Associer les fichiers .wsquashfs au lanceur (double-clic) ?", true) {
		return
	}
	if dryRun {
		fmt.Println("    [simulation] " + filepath.Join(mimeDir, "packages/wsquashfs.xml"))
		return
	}
	if err := os.MkdirAll(filepath.Join(mimeDir, "packages"), 0o755); err != nil {
		printError("Type MIME impossible : " + err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(mimeDir, "packages/wsquashfs.xml"), []byte(mimeXML), 0o644); err != nil {
		printError("Type MIME impossible : " + err.Error())
		return
	}
	if _, err := exec.LookPath("update-mime-database"); err == nil {
		_ = exec.Command("update-mime-database", mimeDir).Run()
	}
	printSuccess("Type MIME créé")
	createDesktopEntry()
}

// createDesktopEntry : le double-clic lance le lanceur installé.
func createDesktopEntry() {
	if dryRun {
		fmt.Println("    [simulation] " + desktopFile + " (Exec=" + installDir + "/wsquashfs-launcher %f)")
		return
	}
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=WSquashFS Launcher
Comment=Lance un jeu .wsquashfs
Exec=%s/wsquashfs-launcher %%f
MimeType=application/x-wsquashfs;application/vnd.squashfs;
NoDisplay=true
Terminal=false
`, installDir)
	if err := os.MkdirAll(filepath.Dir(desktopFile), 0o755); err != nil {
		printError("Entrée du bureau impossible : " + err.Error())
		return
	}
	if err := os.WriteFile(desktopFile, []byte(content), 0o644); err != nil {
		printError("Entrée du bureau impossible : " + err.Error())
		return
	}
	dir := filepath.Dir(desktopFile)
	if _, err := exec.LookPath("update-desktop-database"); err == nil {
		_ = exec.Command("update-desktop-database", dir).Run()
	}
	// Application par défaut pour ce type (sans quoi le bureau peut proposer un autre programme)
	if _, err := exec.LookPath("xdg-mime"); err == nil {
		for _, mime := range []string{"application/x-wsquashfs", "application/vnd.squashfs"} {
			_ = exec.Command("xdg-mime", "default", "wsquashfs-launcher.desktop", mime).Run()
		}
	}
	printSuccess("Entrée du bureau : " + desktopFile)
}

// shellRcFile : fichier de configuration du shell de connexion.
func shellRcFile() string {
	home := os.Getenv("HOME")
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		return filepath.Join(home, ".zshrc")
	case "fish":
		return filepath.Join(home, ".config/fish/config.fish")
	case "bash":
		return filepath.Join(home, ".bashrc")
	}
	return ""
}

// checkPath : ajoute installDir au PATH dans le fichier du shell, sur accord.
func checkPath() {
	for _, d := range strings.Split(os.Getenv("PATH"), ":") {
		if d == installDir {
			printSuccess(installDir + " est dans le PATH")
			return
		}
	}
	printInfo(installDir + " n'est pas dans le PATH")
	rc := shellRcFile()
	if rc == "" {
		rc = filepath.Join(os.Getenv("HOME"), ".profile")
	}
	line := `export PATH="$PATH:` + installDir + `"`
	if filepath.Base(os.Getenv("SHELL")) == "fish" {
		line = "fish_add_path " + installDir
	}
	if data, err := os.ReadFile(rc); err == nil && strings.Contains(string(data), line) {
		printSuccess("PATH déjà configuré dans " + rc + " (ouvrir un nouveau terminal)")
		return
	}
	// Par défaut oui : sans cela, le lanceur n'est pas trouvé par son nom
	if !askYesNo("Ajouter "+installDir+" au PATH (dans "+rc+") ?", true) {
		fmt.Println("    À ajouter à votre shell : " + line)
		return
	}
	if dryRun {
		fmt.Println("    [simulation] " + line + " >> " + rc)
		return
	}
	if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
		printError("PATH non modifié : " + err.Error())
		return
	}
	f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		printError("PATH non modifié : " + err.Error())
		return
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n# WSquashFS Launcher\n%s\n", line); err != nil {
		printError("PATH non modifié : " + err.Error())
		return
	}
	printSuccess("PATH mis à jour dans " + rc + " (nouveau terminal pour en profiter)")
}

func showUsageAfterInstall() {
	fmt.Println()
	fmt.Println(cGreen + "╔════════════════════════════════════════╗" + cNC)
	fmt.Println(cGreen + "║       Installation terminée !          ║" + cNC)
	fmt.Println(cGreen + "╚════════════════════════════════════════╝" + cNC)
	fmt.Println()
	fmt.Println("  wsquashfs-launcher /chemin/jeu.wsquashfs")
	fmt.Println("  wsquashfs-launcher --doctor        (vérifier l'installation)")
	fmt.Println("  wsquashfs-launcher --list          (jeux connus et sauvegardes)")
	fmt.Println("  wsquashfs-launcher --help")
	fmt.Println()
}

// ── Agent pour wsquashfs-manager (section 8b) ──────────────────────────

// agentDefaultRoms : premier dossier roms/ usuel qui existe.
func agentDefaultRoms() string {
	home := os.Getenv("HOME")
	for _, d := range []string{"/userdata/roms", filepath.Join(home, "roms"), "/config/userdata/roms", filepath.Join(home, "games/Batocera/roms")} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return filepath.Join(home, "roms")
}

// portFree : vrai si le port TCP peut être ouvert sur cette machine.
func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// writeAgentUnits : socket et service systemd utilisateur de l'agent.
func writeAgentUnits() error {
	if err := os.MkdirAll(agentUnitDir, 0o755); err != nil {
		return err
	}
	socket := fmt.Sprintf(`[Unit]
Description=Socket de wsquashfs-agent (machine de jeu pour wsquashfs-manager)

[Socket]
ListenStream=%d

[Install]
WantedBy=sockets.target
`, agentPort)
	service := fmt.Sprintf(`[Unit]
Description=wsquashfs-agent (à la demande, lancé par sa socket)

[Service]
ExecStart=%s/wsquashfs-launcher --agent
`, installDir)
	if err := os.WriteFile(filepath.Join(agentUnitDir, "wsquashfs-agent.socket"), []byte(socket), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(agentUnitDir, "wsquashfs-agent.service"), []byte(service), 0o644)
}

// installAgent : moteur de l'agent (wsquashfs-agent.py), initialisation et
// socket systemd utilisateur. Le moteur reste celui du dépôt, lancé par le
// lanceur installé.
func installAgent() bool {
	section("Agent pour wsquashfs-manager")
	if os.Geteuid() == 0 {
		printError("L'agent s'installe pour l'utilisateur de la session graphique, pas pour root :")
		printInfo("relancer l'installeur sans sudo avec --agent")
		return false
	}
	roms := askValue("Dossier roms/ de cette machine", agentDefaultRoms())
	rawPort := askValue("Port de l'agent (doit être libre sur cette machine)", strconv.Itoa(agentPort))
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1024 || port > 65535 {
		printError("Port invalide : " + rawPort)
		return false
	}
	agentPort = port
	if !portFree(port) {
		printError(fmt.Sprintf("Port %d déjà utilisé sur cette machine : en choisir un autre (--agent, puis répondre au port)", port))
		return false
	}
	launcher := filepath.Join(installDir, "wsquashfs-launcher")
	if dryRun {
		fmt.Println("    [simulation] " + launcher + " --pair --roms " + roms + " --port " + strconv.Itoa(port) + "  (jeton, certificat, " + agentDir() + ")")
		fmt.Println("    [simulation] socket " + agentUnitDir + fmt.Sprintf("/wsquashfs-agent.socket (port %d) et service", port))
		fmt.Println("    [simulation] systemctl --user enable --now wsquashfs-agent.socket")
		return true
	}
	// Configuration, jeton et certificat : fait par le lanceur lui-même.
	if err := runCmd(launcher, "--pair", "--roms", roms, "--port", strconv.Itoa(port)); err != nil {
		printError("Initialisation de l'agent en échec (voir ci-dessus)")
		return false
	}
	printSuccess("Agent : " + launcher + " --agent (configuration dans " + agentDir() + ")")

	// Ancien agent (script Python) : son moteur et sa commande ne servent plus
	removeLegacyAgent()
	if err := writeAgentUnits(); err != nil {
		printError("Unités de l'agent impossibles : " + err.Error())
		return false
	}
	printSuccess("Unités : " + agentUnitDir + "/wsquashfs-agent.{socket,service}")

	if cmdOK("systemctl", "--user", "daemon-reload") && cmdOK("systemctl", "--user", "enable", "--now", "wsquashfs-agent.socket") {
		printSuccess("Socket activée : l'agent démarre à la première connexion du gestionnaire")
		if !assumeYes && askYesNo("Répondre aussi sans session ouverte (loginctl enable-linger) ?", false) {
			if u, err := user.Current(); err == nil && cmdOK("loginctl", "enable-linger", u.Username) {
				printSuccess("Linger activé")
			} else {
				printError("loginctl enable-linger en échec : à faire à la main")
			}
		}
	} else {
		printInfo("systemd utilisateur indisponible : lancer « " + launcher + " --agent » à la main")
		printInfo("(cas d'un conteneur sans systemd, comme SteamBox)")
	}
	fmt.Println()
	printInfo("Code d'association à coller dans wsquashfs-manager (Réglages → Machine de jeu → Agent réseau) :")
	_ = runCmd(launcher, "--pair")
	return true
}

// removeLegacyAgent : retire le moteur Python de l'ancien agent (ses données
// et sa commande). Les anciens fichiers de configuration sont laissés en place.
func removeLegacyAgent() {
	legacy := filepath.Join(wsqData, "agent")
	if pathExists(legacy) && os.RemoveAll(legacy) == nil {
		printSuccess("Ancien moteur Python de l'agent retiré : " + legacy)
	}
	stale := filepath.Join(installDir, "wsquashfs-agent")
	if strings.Contains(readHead(stale, 40), "python3") && os.Remove(stale) == nil {
		printSuccess("Ancienne commande wsquashfs-agent retirée")
	}
}

// readHead : début (n octets) d'un fichier, "" si illisible.
func readHead(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > n {
		data = data[:n]
	}
	return string(data)
}

// ── Désinstallation (section 9) ────────────────────────────────────────

// removePaths : suppression, seulement affichée en simulation.
func removePaths(paths ...string) error {
	if dryRun {
		fmt.Println("    [simulation] suppression de " + strings.Join(paths, " "))
		return nil
	}
	for _, p := range paths {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

func updateMimeAndDesktopDatabases() {
	if dryRun {
		return
	}
	if _, err := exec.LookPath("update-desktop-database"); err == nil {
		_ = exec.Command("update-desktop-database", filepath.Dir(desktopFile)).Run()
	}
}

func uninstall() {
	printHeader()
	printInfo("Désinstallation de WSquashFS Launcher...")
	if dryRun {
		printInfo("Simulation : rien ne sera supprimé")
	}
	fmt.Println()
	launcher := filepath.Join(installDir, "wsquashfs-launcher")
	if isFile(launcher) {
		if removePaths(launcher) == nil {
			printSuccess("Lanceur supprimé")
		}
	} else {
		printInfo("Lanceur absent de " + installDir)
	}
	if bridge := filepath.Join(installDir, "dualsense-usb-bridge"); pathExists(bridge) {
		_ = removePaths(bridge)
	}
	socket := filepath.Join(agentUnitDir, "wsquashfs-agent.socket")
	if isFile(socket) {
		if !dryRun {
			_ = exec.Command("systemctl", "--user", "disable", "--now", "wsquashfs-agent.socket").Run()
		}
		_ = removePaths(socket, filepath.Join(agentUnitDir, "wsquashfs-agent.service"))
		if !dryRun {
			_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		}
		printSuccess("Unités de l'agent supprimées")
	}
	removeLegacyAgent()
	stale := filepath.Join(installDir, "wsquashfs-agent")
	if strings.Contains(readHead(stale, 40), "python3") {
		_ = removePaths(stale)
	}
	umuLink := filepath.Join(installDir, "umu-run")
	if target, err := os.Readlink(umuLink); err == nil && target == filepath.Join(umuInstallDir, "umu-run") {
		if removePaths(umuLink, umuInstallDir) == nil {
			printSuccess("umu-run supprimé")
		}
	}
	if isFile(desktopFile) {
		_ = removePaths(desktopFile)
		updateMimeAndDesktopDatabases()
		printSuccess("Entrée du bureau supprimée")
	}
	mimeFile := filepath.Join(os.Getenv("HOME"), ".local/share/mime/packages/wsquashfs.xml")
	if isFile(mimeFile) {
		_ = removePaths(mimeFile)
		if !dryRun {
			if _, err := exec.LookPath("update-mime-database"); err == nil {
				_ = exec.Command("update-mime-database", filepath.Dir(filepath.Dir(mimeFile))).Run()
			}
		}
		printSuccess("Type MIME supprimé")
	}
	fmt.Println()
	printInfo("Conservés (sauvegardes, cache, runners, DXVK/VKD3D/d7vk, archives) :")
	fmt.Println("  " + wsqData + "/")
	fmt.Println("  ~/.cache/wsquashfs/")
	if pathExists(agentDir()) {
		fmt.Println("  " + agentDir() + "/ (jeton et certificat de l'agent)")
	}
	fmt.Println("Conservé (partagé avec Steam et Heroic) : " + protonInstallDir + "/")
	fmt.Println("Paquets système conservés.")
	fmt.Println()
	fmt.Println("Pour tout supprimer : rm -rf " + wsqData + "/ ~/.cache/wsquashfs/")
	fmt.Println()
}

// pathExists : chemin présent, lien cassé compris.
func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// ── Point d'entrée (section 10) ────────────────────────────────────────

const setupUsage = `Usage : wsquashfs-launcher --setup [--yes] [--dry-run] [--agent] [--uninstall] [--help]

  --setup            installe wsquashfs-launcher et ce dont il a besoin
                     (paquets système, runners Wine et Proton, DXVK, VKD3D,
                     d7vk, NVAPI, FSR4, entrée de bureau, PATH).
  --yes, -y          répond oui à toutes les questions (installation complète)
  --dry-run, -n      montre ce qui serait fait, sans rien installer ni modifier
  --agent            installe l'agent pour wsquashfs-manager (sans question)
  --uninstall, -u    désinstalle le lanceur (sauvegardes et runners gardés)
  --help, -h         cette aide

Distributions prises en charge pour les paquets système :
  - Debian, Ubuntu et dérivés (apt)
  - Arch, CachyOS, Manjaro, EndeavourOS et dérivés (pacman)
Ailleurs, la liste des paquets à installer à la main est affichée ; le reste
(runners Wine/Proton, DXVK, VKD3D, d7vk, lanceur) est téléchargé.
`

// installAll : enchaînement de l'installation (install_all de install.sh).
func installAll() int {
	printHeader()
	checkRoot()
	detectDistro()
	printInfo("Distribution : " + distroName + " (" + distro + ")")
	printInfo("Lanceur installé dans : " + installDir)
	if dryRun {
		printInfo("Simulation : rien ne sera installé ni modifié")
	}
	if !checkDependencies() {
		printError("Paquets système incomplets : voir ci-dessus")
		if !askYesNo("Continuer malgré tout ?", false) {
			return 1
		}
	}
	checkWineRunners()
	checkProton()
	checkRendering()
	checkNvapi()
	checkAMD()
	if !installLauncher() {
		return 1
	}
	createMimeType()
	if agentWanted {
		if !installAgent() {
			return 1
		}
	} else if !assumeYes && askYesNo("Installer l'agent pour wsquashfs-manager (machine de jeu sur le réseau) ?", false) {
		if !installAgent() {
			printError("Agent non installé")
		}
	}
	checkPath()
	showUsageAfterInstall()
	// Bilan réel de ce qui est en place
	launcher := filepath.Join(installDir, "wsquashfs-launcher")
	if !dryRun && isFile(launcher) {
		section("Vérification (wsquashfs-launcher --doctor)")
		_ = runCmd(launcher, "--doctor")
	}
	return 0
}

// runSetup : options de --setup, puis installation ou désinstallation.
func runSetup(args []string) int {
	action := "install"
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			assumeYes = true
		case "--dry-run", "-n":
			dryRun = true
		case "--agent":
			agentWanted = true
		case "--uninstall", "-u":
			action = "uninstall"
		case "--help", "-h":
			action = "help"
		default:
			printError("Option inconnue : " + a)
			fmt.Print(setupUsage)
			return 1
		}
	}
	setupInit()
	switch action {
	case "help":
		fmt.Print(setupUsage)
		return 0
	case "uninstall":
		checkRoot()
		uninstall()
		return 0
	}
	return installAll()
}
