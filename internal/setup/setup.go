// Package setup : installeur intégré (--setup), qui porte install.sh. Paquets
// système, runners Wine et Proton, DXVK/VKD3D/d7vk, NVAPI, FSR4, lanceur, type
// MIME, entrée de bureau, PATH, complétion bash et désinstallation. Le paquet
// n'arrête jamais le processus : Run renvoie le code de sortie.
package setup

// Sections 8 (lanceur, MIME, bureau, PATH), 8b (agent pour wsquashfs-manager),
// 9 (désinstallation) et 10 (enchaînement) de install.sh.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"wsquashfs-launcher/internal/filetree"
	"wsquashfs-launcher/internal/ui"
)

// Env : ce que l'installeur reçoit du lanceur.
type Env struct {
	AgentDir         string                       // dossier de configuration de l'agent (conservé à la désinstallation)
	TrayAutostart    string                       // fichier de lancement automatique de l'icône de notification
	CoverPath        string                       // illustration dans l'image, lue par le fichier de miniatures
	CompletionScript string                       // script de complétion bash
	RemoveAgentUnits func() bool                  // retire le démarrage systemd des anciennes versions de l'agent
	PruneOldProtons  func(family string) []string // retire les anciennes versions d'une famille de Proton, renvoie leurs noms
}

// ShortName : nom court du lanceur (« ws --update »), lien vers le binaire installé.
const ShortName = "ws"

// env : réglages de l'exécution en cours (fixés par Run).
var env Env

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
	optiscalerDir    string
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
	optiscalerDir = filepath.Join(wsqData, "optiscaler")
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
	if err := filetree.CopyFile(exePath(), tmp, 0o755); err != nil {
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

// shortNameTaken : « ws » est déjà une autre commande (fichier, lien étranger
// dans installDir, ou commande du PATH).
type shortNameTaken struct{ by string }

func (e shortNameTaken) Error() string {
	return "ws déjà pris par " + e.by + " : nom court non posé"
}

// isOwnShortName : link est un lien symbolique vers target (ou vers le même
// fichier). Un fichier ordinaire ou un lien étranger n'est jamais le nôtre.
func isOwnShortName(link, target string) bool {
	lt, err := os.Readlink(link)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(lt) {
		lt = filepath.Join(filepath.Dir(link), lt)
	}
	return filepath.Clean(lt) == filepath.Clean(target) || sameFile(link, target)
}

// checkShortName : sans rien écrire. true si installDir/ws reste à poser,
// false s'il est déjà notre lien, shortNameTaken s'il est pris par une autre
// commande (dans installDir ou ailleurs dans le PATH).
func checkShortName(installDir, target string) (bool, error) {
	link := filepath.Join(installDir, ShortName)
	if _, err := os.Lstat(link); err == nil {
		if isOwnShortName(link, target) {
			return false, nil
		}
		by := link
		if lt, err := os.Readlink(link); err == nil {
			by = link + " → " + lt
		}
		return false, shortNameTaken{by: by}
	}
	if found, err := exec.LookPath(ShortName); err == nil && !sameFile(found, target) {
		return false, shortNameTaken{by: found}
	}
	return true, nil
}

// installShortName : pose installDir/ws → target, sans jamais écraser une autre
// commande. Retourne true si le lien est créé, false s'il était déjà en place.
// Lien relatif quand le lanceur est dans installDir (le dossier peut bouger).
func installShortName(installDir, target string) (bool, error) {
	created, err := checkShortName(installDir, target)
	if err != nil || !created {
		return false, err
	}
	dst := target
	if filepath.Dir(target) == installDir {
		dst = filepath.Base(target)
	}
	return true, os.Symlink(dst, filepath.Join(installDir, ShortName))
}

// installShortNameLink : étape Lanceur, après le binaire. En simulation, rien
// n'est écrit : seulement ce qui serait fait ou ce qui bloque.
func installShortNameLink(target string) {
	link := filepath.Join(installDir, ShortName)
	if dryRun {
		created, err := checkShortName(installDir, target)
		var taken shortNameTaken
		switch {
		case errors.As(err, &taken):
			setupSkip(err.Error(), "wsquashfs-launcher reste la commande")
		case err != nil:
			setupFail("Nom court ws impossible : " + err.Error())
		case created:
			simLine(link + " → " + target)
		default:
			setupHave("Nom court ws déjà en place : " + link)
		}
		return
	}
	created, err := installShortName(installDir, target)
	var taken shortNameTaken
	switch {
	case errors.As(err, &taken):
		setupSkip(err.Error(), "wsquashfs-launcher reste la commande")
	case err != nil:
		setupFail("Nom court ws impossible : " + err.Error())
	case created:
		setupDone("Nom court : " + link + " → " + target)
	default:
		setupHave("Nom court ws déjà en place : " + link)
	}
}

// installLauncher : ce binaire s'installe lui-même (aucun téléchargement :
// la version installée est celle qui tourne, « --update » la met à jour),
// puis le nom court.
func installLauncher() bool {
	dest := filepath.Join(installDir, "wsquashfs-launcher")
	if err := installSelf(dest); err != nil {
		setupFail("Installation du lanceur impossible : " + err.Error())
		return false
	}
	setupDone("Lanceur : " + dest)
	installShortNameLink(dest)
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
		"Exec=sh -c 'unsquashfs -cat \"$0\" " + env.CoverPath + " > \"$1\"' %i %o\n" +
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
		ui.Note("À ajouter à votre shell :")
		ui.Code(line)
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

// removeLegacyAgent : retire le moteur Python de l'ancien agent (ses données
// et sa commande). Les anciens fichiers de configuration sont laissés en place.
func removeLegacyAgent() {
	legacy := filepath.Join(wsqData, "agent")
	if pathExists(legacy) && os.RemoveAll(legacy) == nil {
		ui.Item(ui.OK, "Ancien moteur Python de l'agent retiré : "+legacy)
	}
	stale := filepath.Join(installDir, "wsquashfs-agent")
	if strings.Contains(readHead(stale, 40), "python3") && os.Remove(stale) == nil {
		ui.Item(ui.OK, "Ancienne commande wsquashfs-agent retirée")
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
	ui.Title("Désinstallation de WSquashFS Launcher", sub)
	launcher := filepath.Join(installDir, "wsquashfs-launcher")
	short := filepath.Join(installDir, ShortName)
	switch {
	case isOwnShortName(short, launcher):
		if removePaths(short) == nil {
			ui.Item(ui.OK, "Nom court ws retiré")
		}
	case pathExists(short):
		ui.Item(ui.Skip, "ws laissé : autre commande que ce lanceur")
	}
	if isFile(launcher) {
		if removePaths(launcher) == nil {
			ui.Item(ui.OK, "Lanceur supprimé")
		}
	} else {
		ui.Item(ui.Skip, "Lanceur absent de "+installDir)
	}
	// Ancien pont DualSense (script), remplacé par la commande interne du lanceur.
	if bridge := filepath.Join(installDir, "dualsense-usb-bridge"); pathExists(bridge) {
		_ = removePaths(bridge)
	}
	if !dryRun {
		env.RemoveAgentUnits()
	}
	removeLegacyAgent()
	stale := filepath.Join(installDir, "wsquashfs-agent")
	if strings.Contains(readHead(stale, 40), "python3") {
		_ = removePaths(stale)
	}
	umuLink := filepath.Join(installDir, "umu-run")
	if target, err := os.Readlink(umuLink); err == nil && target == filepath.Join(umuInstallDir, "umu-run") {
		if removePaths(umuLink, umuInstallDir) == nil {
			ui.Item(ui.OK, "umu-run supprimé")
		}
	}
	if !dryRun && removeCompletion() {
		ui.Item(ui.OK, "Complétion bash supprimée")
	}
	if isFile(desktopFile) {
		_ = removePaths(desktopFile)
		updateMimeAndDesktopDatabases()
		ui.Item(ui.OK, "Entrée du bureau supprimée")
	}
	if isFile(thumbnailerFile()) {
		_ = removePaths(thumbnailerFile())
		ui.Item(ui.OK, "Miniatures des .wsquashfs supprimées")
	}
	if isFile(env.TrayAutostart) {
		_ = removePaths(env.TrayAutostart)
		ui.Item(ui.OK, "Icône de zone de notification : lancement à l'ouverture de session supprimé")
	}
	mimeFile := filepath.Join(os.Getenv("HOME"), ".local/share/mime/packages/wsquashfs.xml")
	if isFile(mimeFile) {
		_ = removePaths(mimeFile)
		if !dryRun {
			if _, err := exec.LookPath("update-mime-database"); err == nil {
				_ = exec.Command("update-mime-database", filepath.Dir(filepath.Dir(mimeFile))).Run()
			}
		}
		ui.Item(ui.OK, "Type MIME supprimé")
	}
	kept := []string{wsqData + "/ (sauvegardes, runners, DXVK/VKD3D/d7vk, archives)", "~/.cache/wsquashfs/ (cache)"}
	if pathExists(env.AgentDir) {
		kept = append(kept, env.AgentDir+"/ (machines associées et certificat de l'agent)")
	}
	kept = append(kept, protonInstallDir+"/ (partagé avec Steam et Heroic)")
	ui.List("Conservés", kept)
	ui.Note("Paquets système conservés.")
	ui.Note("Pour tout supprimer :")
	ui.Code("rm -rf " + wsqData + "/ ~/.cache/wsquashfs/")
}

// pathExists : chemin présent, lien cassé compris.
func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// ── Point d'entrée (section 10) ────────────────────────────────────────

const setupUsage = `Usage : wsquashfs-launcher --setup [yes] [dry-run] [uninstall] [proton-cachyos] [ge-proton] [umu] [optiscaler] [help]

  --setup            installe wsquashfs-launcher et ce dont il a besoin
                     (paquets système, runners Wine et Proton, DXVK, VKD3D,
                     d7vk, NVAPI, FSR4, OptiScaler, entrée de bureau, PATH).
  yes                répond oui à toutes les questions (installation complète)
  dry-run            montre ce qui serait fait, sans rien installer ni modifier
  uninstall          désinstalle le lanceur (sauvegardes et runners gardés)
  proton-cachyos     installe seulement la dernière version de proton-cachyos,
                     le Proton par défaut (release GitHub de CachyOS, somme
                     SHA-512 vérifiée, x86-64-v3 si le processeur le permet) ;
                     à relancer pour passer à une version plus récente, les
                     précédentes sont retirées
  ge-proton          installe seulement la dernière version de GE-Proton (somme
                     SHA-512 vérifiée), choisie par PROTON=GE-Proton ; à relancer
                     pour passer à une version plus récente, les précédentes sont
                     retirées
  umu                installe ou met à jour seulement umu-run (zipapp GitHub,
                     version la plus récente ; paquet pacman sur Arch, mis à jour
                     par pacman -Syu) et UMU-Proton, dont les précédentes
                     versions sont retirées
  optiscaler         installe ou met à jour seulement OptiScaler (release
                     GitHub, somme SHA-256 vérifiée), activé jeu par jeu par
                     OPTISCALER=1 hors de l'image (surcharge, dossier, machine)
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
	ui.Title("Installation de WSquashFS Launcher", sub)

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
	setupStage("OptiScaler (upscaler et génération d'images)")
	checkOptiscaler()
	setupStage("Lanceur")
	if !installLauncher() {
		return setupEnd("Installation interrompue : lanceur non installé")
	}
	setupStage("Association des .wsquashfs et entrée de bureau")
	createMimeType()
	if !dryRun {
		// Restes des anciennes versions de l'agent : démarrage automatique par systemd (port
		// ouvert à chaque session), moteur Python. L'agent se lance à la main : --pair, --agent.
		env.RemoveAgentUnits()
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
	ui.Section("Bilan")
	installedKey := "Installés"
	if dryRun {
		installedKey = "À installer (simulation)"
	}
	ui.KV(2,
		ui.Pair{installedKey, strconv.Itoa(setupCount.installed)},
		ui.Pair{"Déjà présents", strconv.Itoa(setupCount.present)},
		ui.Pair{"Ignorés", strconv.Itoa(setupCount.skipped)},
		ui.Pair{"Échecs", strconv.Itoa(setupCount.failed)},
	)
	if abort != "" {
		ui.Verdict(false, abort)
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
	ui.Verdict(ok, msg)
	ui.Out("")
	ui.Note("Commandes utiles :")
	ui.Code("wsquashfs-launcher /chemin/jeu.wsquashfs")
	ui.Code("wsquashfs-launcher --doctor")
	ui.Code("wsquashfs-launcher --list")
	ui.Code("wsquashfs-launcher --help")
	return 0
}

// Run : options de --setup (args), puis installation ou désinstallation.
// Retourne le code de sortie.
func Run(args []string, e Env) int {
	env = e
	dryRun, assumeYes = false, false
	stageN, stageTotal, setupCount = 0, 0, struct{ installed, present, skipped, failed int }{}
	action := "install"
	for _, a := range args {
		switch a {
		case "yes":
			assumeYes = true
		case "dry-run":
			dryRun = true
		case "uninstall":
			action = "uninstall"
		case "ge-proton", "proton-cachyos", "optiscaler", "umu":
			action = a
		case "help":
			action = "help"
		default:
			ui.Error("Option inconnue pour --setup : " + a + " (yes, dry-run, uninstall, proton-cachyos, ge-proton, umu, optiscaler, help)")
			ui.Help(setupUsage)
			return 1
		}
	}
	setupInit()
	switch action {
	case "help":
		ui.Help(setupUsage)
		return 0
	case "uninstall":
		checkRoot()
		uninstall()
		return 0
	case "ge-proton":
		checkRoot()
		stageTotal = 1
		setupStage("GE-Proton")
		if !downloadGEProton() {
			return setupEnd("GE-Proton non installé")
		}
		// Une seule version de GE-Proton à la fois : les précédentes ne servent plus
		// (un prefix qui les utilisait passe à la plus récente, findProtonRunner).
		if !dryRun && env.PruneOldProtons != nil {
			if removed := env.PruneOldProtons("GE-Proton"); len(removed) > 0 {
				setupDone("Anciennes versions retirées : " + strings.Join(removed, ", "))
			}
		}
		return setupEnd("")
	case "umu":
		checkRoot()
		detectDistro()
		stageTotal = 1
		setupStage("umu-run")
		if !updateUmu() {
			return setupEnd("umu-run non installé")
		}
		// UMU-Proton (Proton de repli de umu) : dernière version, anciennes retirées.
		if !downloadUmuProton() {
			return setupEnd("UMU-Proton non installé")
		}
		if !dryRun && env.PruneOldProtons != nil {
			if removed := env.PruneOldProtons("UMU-Proton"); len(removed) > 0 {
				setupDone("Anciennes versions retirées : " + strings.Join(removed, ", "))
			}
		}
		return setupEnd("")
	case "optiscaler":
		checkRoot()
		stageTotal = 1
		setupStage("OptiScaler")
		if !downloadOptiscaler() {
			return setupEnd("OptiScaler non installé")
		}
		return setupEnd("")
	case "proton-cachyos":
		checkRoot()
		stageTotal = 1
		setupStage("proton-cachyos")
		if !downloadProtonCachyos() {
			return setupEnd("proton-cachyos non installé")
		}
		// Comme GE-Proton : les précédentes versions ne servent plus.
		if !dryRun && env.PruneOldProtons != nil {
			if removed := env.PruneOldProtons("proton-cachyos"); len(removed) > 0 {
				setupDone("Anciennes versions retirées : " + strings.Join(removed, ", "))
			}
		}
		return setupEnd("")
	}
	return installAll()
}
