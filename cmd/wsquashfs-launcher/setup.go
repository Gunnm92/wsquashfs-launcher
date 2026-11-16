package main

// Installeur intégré (--setup) : porte install.sh. Sections 8 (lanceur, MIME,
// bureau, PATH), 8b (agent pour wsquashfs-manager), 9 (désinstallation) et
// 10 (enchaînement).

import (
	"fmt"
	"os"
	"os/exec"
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
)

// setupInit : calcule les emplacements à partir de HOME.
func setupInit() {
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
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
		simLine(rel + " → " + dest)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.new.%d", dest, os.Getpid())
	if err := downloadTo("https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main/"+rel, tmp); err != nil {
		os.Remove(tmp)
		setupErr("Téléchargement de " + rel + " impossible")
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// installSelf : le lanceur installé est ce binaire lui-même (rien à faire
// s'il tourne déjà depuis dest). Copie voisine renommée ensuite : un lanceur
// en cours d'exécution garde son fichier.
func installSelf(dest string) error {
	if dryRun {
		simLine(exePath() + " → " + dest)
		return nil
	}
	if sameFile(exePath(), dest) {
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

// sameFile : deux chemins du même fichier (liens résolus).
func sameFile(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(sa, sb)
}

// installBridge : pont DualSense, pris à côté du binaire (dépôt cloné) ;
// sinon téléchargé du dépôt.
func installBridge(dest string) error {
	const rel = "tools/dualsense-usb-bridge/dualsense-usb-bridge"
	local := filepath.Join(filepath.Dir(exePath()), rel)
	if !isFile(local) {
		return installRepoFile(rel, dest)
	}
	if dryRun {
		simLine(local + " → " + dest)
		return nil
	}
	tmp := fmt.Sprintf("%s.new.%d", dest, os.Getpid())
	if err := copyFile(local, tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// installLauncher : ce binaire s'installe lui-même (aucun téléchargement :
// la version installée est celle qui tourne, « --update » la met à jour),
// puis le pont DualSense.
func installLauncher() bool {
	dest := filepath.Join(installDir, "wsquashfs-launcher")
	if err := installSelf(dest); err != nil {
		setupFail("Installation du lanceur impossible : " + err.Error())
		return false
	}
	setupDone("Lanceur : " + dest)
	bridge := filepath.Join(installDir, "dualsense-usb-bridge")
	if installBridge(bridge) == nil && !dryRun {
		setupDone("Pont DualSense : " + bridge)
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
		setupSkip("Association des .wsquashfs non faite")
		return
	}
	if dryRun {
		simLine(filepath.Join(mimeDir, "packages/wsquashfs.xml"))
		createDesktopEntry()
		createThumbnailer()
		return
	}
	if err := os.MkdirAll(filepath.Join(mimeDir, "packages"), 0o755); err != nil {
		setupFail("Type MIME impossible : " + err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(mimeDir, "packages/wsquashfs.xml"), []byte(mimeXML), 0o644); err != nil {
		setupFail("Type MIME impossible : " + err.Error())
		return
	}
	if _, err := exec.LookPath("update-mime-database"); err == nil {
		_ = exec.Command("update-mime-database", mimeDir).Run()
	}
	setupDone("Type MIME créé")
	createDesktopEntry()
	createThumbnailer()
}

// imageCoverPath : illustration du jeu dans l'image (jaquette ou icône, tout
// format lu par gdk-pixbuf : PNG, JPEG, ICO, WebP), déposée par --pack (depuis
// le dossier source) ou par la reconstruction du manager.
const imageCoverPath = ".wsquashfs/cover"

// thumbnailerFile : miniatures des .wsquashfs (spécification freedesktop, lue
// par tumbler sous XFCE, Nautilus, Nemo, Caja ; pas par Dolphin).
func thumbnailerFile() string {
	return filepath.Join(os.Getenv("HOME"), ".local/share/thumbnailers/wsquashfs.thumbnailer")
}

// thumbnailerContent : unsquashfs -cat lit l'illustration sans monter l'image
// (outils de /usr seulement : le bac à sable de GNOME ne voit pas le lanceur).
// [X-Tumbler Settings] : tumbler (Thunar) ignore sinon les fichiers de plus de
// 2 Gio ; les autres gestionnaires ignorent ce groupe.
func thumbnailerContent() string {
	return "[Thumbnailer Entry]\n" +
		"TryExec=unsquashfs\n" +
		"Exec=sh -c 'unsquashfs -cat \"$0\" " + imageCoverPath + " > \"$1\"' %i %o\n" +
		"MimeType=application/x-wsquashfs;\n" +
		"\n" +
		"[X-Tumbler Settings]\n" +
		"MaxFileSize=0\n" // tumbler ≥ 4.17 : pas de limite de 2 Gio (images de jeux)
}

// createThumbnailer : miniature des .wsquashfs dans le gestionnaire de
// fichiers. Les échecs déjà mémorisés (avant le fichier) sont oubliés.
func createThumbnailer() {
	f := thumbnailerFile()
	if dryRun {
		simLine(f)
		return
	}
	if old, err := os.ReadFile(f); err == nil && string(old) == thumbnailerContent() {
		setupDone("Miniatures : " + f)
		return
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		setupFail("Miniatures impossibles : " + err.Error())
		return
	}
	if err := os.WriteFile(f, []byte(thumbnailerContent()), 0o644); err != nil {
		setupFail("Miniatures impossibles : " + err.Error())
		return
	}
	_ = os.RemoveAll(filepath.Join(os.Getenv("HOME"), ".cache/thumbnails/fail"))
	setupDone("Miniatures : " + f)
}

// createDesktopEntry : le double-clic lance le lanceur installé.
func createDesktopEntry() {
	if dryRun {
		simLine(desktopFile + " (Exec=" + installDir + "/wsquashfs-launcher %f)")
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
		setupFail("Entrée du bureau impossible : " + err.Error())
		return
	}
	if err := os.WriteFile(desktopFile, []byte(content), 0o644); err != nil {
		setupFail("Entrée du bureau impossible : " + err.Error())
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
	setupDone("Entrée du bureau : " + desktopFile)
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
			setupHave(installDir + " est dans le PATH")
			return
		}
	}
	setupTodo(installDir + " n'est pas dans le PATH")
	rc := shellRcFile()
	if rc == "" {
		rc = filepath.Join(os.Getenv("HOME"), ".profile")
	}
	line := `export PATH="$PATH:` + installDir + `"`
	if filepath.Base(os.Getenv("SHELL")) == "fish" {
		line = "fish_add_path " + installDir
	}
	if data, err := os.ReadFile(rc); err == nil && strings.Contains(string(data), line) {
		setupHave("PATH déjà configuré dans " + rc + " (ouvrir un nouveau terminal)")
		return
	}
	// Par défaut oui : sans cela, le lanceur n'est pas trouvé par son nom
	if !askYesNo("Ajouter "+installDir+" au PATH (dans "+rc+") ?", true) {
		setupSkip("PATH non modifié")
		uiNote("À ajouter à votre shell :")
		uiCode(line)
		return
	}
	if dryRun {
		simLine(line + " >> " + rc)
		return
	}
	if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
		setupFail("PATH non modifié : " + err.Error())
		return
	}
	f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		setupFail("PATH non modifié : " + err.Error())
		return
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n# WSquashFS Launcher\n%s\n", line); err != nil {
		setupFail("PATH non modifié : " + err.Error())
		return
	}
	setupDone("PATH mis à jour dans " + rc + " (nouveau terminal pour en profiter)")
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

// removeLegacyAgent : retire le moteur Python de l'ancien agent (ses données
// et sa commande). Les anciens fichiers de configuration sont laissés en place.
func removeLegacyAgent() {
	legacy := filepath.Join(wsqData, "agent")
	if pathExists(legacy) && os.RemoveAll(legacy) == nil {
		uiItem(stOK, "Ancien moteur Python de l'agent retiré : "+legacy)
	}
	stale := filepath.Join(installDir, "wsquashfs-agent")
	if strings.Contains(readHead(stale, 40), "python3") && os.Remove(stale) == nil {
		uiItem(stOK, "Ancienne commande wsquashfs-agent retirée")
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
		simLine("suppression de " + strings.Join(paths, " "))
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
	sub := ""
	if dryRun {
		sub = "simulation : rien n'est supprimé"
	}
	uiTitle("Désinstallation de WSquashFS Launcher", sub)
	launcher := filepath.Join(installDir, "wsquashfs-launcher")
	if isFile(launcher) {
		if removePaths(launcher) == nil {
			uiItem(stOK, "Lanceur supprimé")
		}
	} else {
		uiItem(stSkip, "Lanceur absent de "+installDir)
	}
	if bridge := filepath.Join(installDir, "dualsense-usb-bridge"); pathExists(bridge) {
		_ = removePaths(bridge)
	}
	if !dryRun {
		removeAgentUnits()
	}
	removeLegacyAgent()
	stale := filepath.Join(installDir, "wsquashfs-agent")
	if strings.Contains(readHead(stale, 40), "python3") {
		_ = removePaths(stale)
	}
	umuLink := filepath.Join(installDir, "umu-run")
	if target, err := os.Readlink(umuLink); err == nil && target == filepath.Join(umuInstallDir, "umu-run") {
		if removePaths(umuLink, umuInstallDir) == nil {
			uiItem(stOK, "umu-run supprimé")
		}
	}
	if !dryRun && removeCompletion() {
		uiItem(stOK, "Complétion bash supprimée")
	}
	if isFile(desktopFile) {
		_ = removePaths(desktopFile)
		updateMimeAndDesktopDatabases()
		uiItem(stOK, "Entrée du bureau supprimée")
	}
	if isFile(thumbnailerFile()) {
		_ = removePaths(thumbnailerFile())
		uiItem(stOK, "Miniatures des .wsquashfs supprimées")
	}
	if isFile(trayAutostartPath()) {
		_ = removePaths(trayAutostartPath())
		uiItem(stOK, "Icône de zone de notification : lancement à l'ouverture de session supprimé")
	}
	mimeFile := filepath.Join(os.Getenv("HOME"), ".local/share/mime/packages/wsquashfs.xml")
	if isFile(mimeFile) {
		_ = removePaths(mimeFile)
		if !dryRun {
			if _, err := exec.LookPath("update-mime-database"); err == nil {
				_ = exec.Command("update-mime-database", filepath.Dir(filepath.Dir(mimeFile))).Run()
			}
		}
		uiItem(stOK, "Type MIME supprimé")
	}
	kept := []string{wsqData + "/ (sauvegardes, runners, DXVK/VKD3D/d7vk, archives)", "~/.cache/wsquashfs/ (cache)"}
	if pathExists(agentDir()) {
		kept = append(kept, agentDir()+"/ (machines associées et certificat de l'agent)")
	}
	kept = append(kept, protonInstallDir+"/ (partagé avec Steam et Heroic)")
	uiList("Conservés", kept)
	uiNote("Paquets système conservés.")
	uiNote("Pour tout supprimer :")
	uiCode("rm -rf " + wsqData + "/ ~/.cache/wsquashfs/")
}

// pathExists : chemin présent, lien cassé compris.
func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// ── Point d'entrée (section 10) ────────────────────────────────────────

const setupUsage = `Usage : wsquashfs-launcher --setup [yes] [dry-run] [uninstall] [help]

  --setup            installe wsquashfs-launcher et ce dont il a besoin
                     (paquets système, runners Wine et Proton, DXVK, VKD3D,
                     d7vk, NVAPI, FSR4, entrée de bureau, PATH).
  yes                répond oui à toutes les questions (installation complète)
  dry-run            montre ce qui serait fait, sans rien installer ni modifier
  uninstall          désinstalle le lanceur (sauvegardes et runners gardés)
  help               cette aide

Distributions prises en charge pour les paquets système :
  - Debian, Ubuntu et dérivés (apt)
  - Arch, CachyOS, Manjaro, EndeavourOS et dérivés (pacman)
Ailleurs, la liste des paquets à installer à la main est affichée ; le reste
(runners Wine/Proton, DXVK, VKD3D, d7vk, lanceur) est téléchargé.
`

// installAll : enchaînement de l'installation (install_all de install.sh).
func installAll() int {
	checkRoot()
	detectDistro()
	stageTotal = setupPlanTotal()
	sub := "Distribution : " + distroName + " · lanceur dans " + installDir
	if dryRun {
		sub += " · simulation : rien n'est modifié"
	}
	uiTitle("Installation de WSquashFS Launcher", sub)

	setupStage("Paquets système (" + distroName + ")")
	if !checkDependencies() {
		setupErr("Paquets système incomplets : voir ci-dessus")
		if !askYesNo("Continuer malgré tout ?", false) {
			return setupEnd("Installation interrompue : paquets système incomplets")
		}
	}
	setupStage("Wine pour les jeux arcade Batocera")
	checkWineRunners()
	setupStage("Proton et umu (jeux PC récents)")
	checkProton()
	setupStage("Rendu : DXVK, VKD3D-Proton, d7vk")
	checkRendering()
	if hasGPU("0x10de") {
		setupStage("NVIDIA : DXVK-NVAPI (NVAPI, DLSS, couche Reflex)")
		checkNvapi()
	}
	if hasGPU("0x1002") {
		setupStage("AMD : RADV et FSR4")
		checkAMD()
	}
	setupStage("Lanceur")
	if !installLauncher() {
		return setupEnd("Installation interrompue : lanceur non installé")
	}
	setupStage("Association des .wsquashfs et entrée de bureau")
	createMimeType()
	if !dryRun {
		// Restes des anciennes versions de l'agent : démarrage automatique par systemd (port
		// ouvert à chaque session), moteur Python. L'agent se lance à la main : --pair, --agent.
		removeAgentUnits()
		removeLegacyAgent()
	}
	setupStage("PATH du shell")
	checkPath()
	checkCompletion()
	setupStage("Vérification (--doctor)")
	launcher := filepath.Join(installDir, "wsquashfs-launcher")
	switch {
	case dryRun:
		setupSkip("--doctor non lancé en simulation")
	case !isFile(launcher):
		setupErr("lanceur absent de " + installDir + " : --doctor non lancé")
	default:
		_ = runCmd(launcher, "--doctor")
	}
	return setupEnd("")
}

// setupEnd : bilan du parcours, puis verdict et commandes utiles. abort : motif
// d'un arrêt en cours de route (code 1, sans commandes utiles).
func setupEnd(abort string) int {
	uiSection("Bilan")
	installedKey := "Installés"
	if dryRun {
		installedKey = "À installer (simulation)"
	}
	uiKV(2,
		kv{installedKey, strconv.Itoa(setupCount.installed)},
		kv{"Déjà présents", strconv.Itoa(setupCount.present)},
		kv{"Ignorés", strconv.Itoa(setupCount.skipped)},
		kv{"Échecs", strconv.Itoa(setupCount.failed)},
	)
	if abort != "" {
		uiVerdict(false, abort)
		return 1
	}
	msg := "Installation terminée"
	if dryRun {
		msg = "Simulation terminée"
	}
	ok := setupCount.failed == 0
	if !ok {
		msg += fmt.Sprintf(" avec %d échec(s)", setupCount.failed)
	}
	uiVerdict(ok, msg)
	uiOut("")
	uiNote("Commandes utiles :")
	uiCode("wsquashfs-launcher /chemin/jeu.wsquashfs")
	uiCode("wsquashfs-launcher --doctor")
	uiCode("wsquashfs-launcher --list")
	uiCode("wsquashfs-launcher --help")
	return 0
}

// runSetup : options de --setup, puis installation ou désinstallation.
func runSetup(args []string) int {
	action := "install"
	for _, a := range args {
		switch a {
		case "yes":
			assumeYes = true
		case "dry-run":
			dryRun = true
		case "uninstall":
			action = "uninstall"
		case "help":
			action = "help"
		default:
			uiError("Option inconnue pour --setup : " + a + " (yes, dry-run, uninstall, help)")
			uiHelp(setupUsage)
			return 1
		}
	}
	setupInit()
	switch action {
	case "help":
		uiHelp(setupUsage)
		return 0
	case "uninstall":
		checkRoot()
		uninstall()
		return 0
	}
	return installAll()
}
