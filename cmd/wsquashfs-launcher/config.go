package main

// Configuration et état du lancement (script bash, sections 1 et 9.1).
// Les valeurs de réglage restent des chaînes ("1", "0", "") comme dans le
// script : les comparaisons s'écrivent alors à l'identique.

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ── Configuration (figée par init_config) ──────────────────────────────
var (
	savesDir        string // couches de sauvegardes (upperdir), une par jeu
	cacheDir        string // montages et copies de travail
	runAsRoot       bool   // lancé en root (sudo)
	targetUser      string // utilisateur qui lance Wine quand on est root (SUDO_USER)
	allowOtherOpt   string // option FUSE allow_other, en root seulement
	wineUID         int    // propriétaire de tout ce que Wine doit pouvoir écrire
	wineGID         int
	overlaySquashOp string // options fuse-overlayfs squash_to_uid/gid
	overlayPref     string // auto | kernel | fuse (WSQUASHFS_OVERLAY)
	selfPath        string // chemin de ce binaire ou script, relancé dans le namespace
	sysroot         string // racine des dossiers système fouillés (tests)
)

// ── Ligne de commande (parse_args) ─────────────────────────────────────
var (
	action       = "run" // run | mount | exec | prepare | help | commande de maintenance
	gameArg      string  // jeu demandé (.wsquashfs ou dossier installé)
	wineOverride string  // --wine <version|chemin>
	setArgs      []string
	headless     bool
	execCmd      []string // --exec : programme et ses arguments
	cmdArgs      []string // arguments d'une commande de maintenance
	origArgs     []string // ligne de commande d'origine (relance dans le namespace)
)

// ── Jeu et montage ─────────────────────────────────────────────────────
var (
	wsquashfsFile   string // chemin absolu du .wsquashfs (ou du dossier installé)
	dirMode         bool   // jeu installé, lancé sur place
	folderOverlay   bool   // dossier .pc essayé en overlay (lecture seule, comme son image)
	gameName        string
	autorunOverride string // <jeu>.wsquashfs.autorun s'il existe
	mountMode       string // overlay | copy | dir
	overlayKind     string // kernel | fuse
	mounted         bool   // cleanup() doit arrêter Wine et démonter
	squashfsPoint   string // image montée (lowerdir)
	winePoint       string // prefix vu par Wine
	savePoint       string // couche de sauvegardes du jeu (upperdir)
	workPoint       string // workdir de l'overlay
	nsStage         string // "1" : relancé dans le namespace de l'overlay noyau
	nsDir           string // dossier d'échange hôte/namespace
	nsPID           int    // lanceur en namespace démarré par ce processus (hôte)
	packagedPrefix  bool   // l'image contient un prefix (system.reg)
	protonPrefix    bool   // prefix créé par Proton (config_info)
	lockFile        *os.File
	lockedGame      string
)

// ── autorun.cmd ────────────────────────────────────────────────────────
var (
	dir, cmd, wineVersion, protonVersion, runnerKey, wineArch string
	imageRunner                                               string
	dxvk, dxvkWanted, vkd3d, d7vk, savedirKey, savefilesKey   string
	esync, fsync, wineLang, wineEnv, virtualDesktop           string
	nvapi, nvapiOn, vkreflex, vkreflexOn                      string
	hasNvidia, hasAMD                                         string
	fsr, fsr4, fsr4On                                         string
	preCmd, attachCmd, attachProcess                          string
	attachPID                                                 int
	machineLayer, folderLayer                                 string
)

// ── Exécutable, runner, contrôleurs ────────────────────────────────────
var (
	execPath       string
	execArgs       string
	execArgsArr    []string
	saveTargets    []string
	ext            string
	runCmdArgs     []string
	wineBinary     string
	prefixProton   string
	overrideProton string
	forcedWine     string
	autoProton     bool
	umuRun         string
	protonDir      string
	wineTool       []string
	hidrawDisable  string
	hidrawMsg      string
	hidrawReason   string
	dsBridgePID    int
	opaqueCache    = map[string]string{}
)

// Lancé par un simple "sudo" : sudo remet HOME=/root et efface
// XDG_RUNTIME_DIR/WAYLAND_DISPLAY. On repart de SUDO_USER (30/09) ; une valeur
// conservée par sudo -E n'est jamais remplacée.
func restoreSudoUserSession() {
	sudoUser := os.Getenv("SUDO_USER")
	if os.Geteuid() != 0 || sudoUser == "" || sudoUser == "root" {
		return
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return
	}
	if home := os.Getenv("HOME"); home == "" || home == "/root" {
		os.Setenv("HOME", u.HomeDir)
	}
	runtime := "/run/user/" + u.Uid
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		if st, err := os.Stat(runtime); err == nil && st.IsDir() {
			os.Setenv("XDG_RUNTIME_DIR", runtime)
		}
	}
	if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("XDG_RUNTIME_DIR") != "" {
		if st, err := os.Stat(os.Getenv("XDG_RUNTIME_DIR") + "/wayland-0"); err == nil && st.Mode()&os.ModeSocket != 0 {
			os.Setenv("WAYLAND_DISPLAY", "wayland-0")
		}
	}
}

// limitStackSize : pile limitée à 8 Mo si illimitée (proton-cachyos ne
// démarre pas avec une disposition mémoire « legacy », 30/09).
func limitStackSize() {
	var rl syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_STACK, &rl) != nil {
		return
	}
	if rl.Cur == ^uint64(0) {
		rl.Cur = 8192 * 1024
		_ = syscall.Setrlimit(syscall.RLIMIT_STACK, &rl)
	}
}

// raiseNofileLimit : descripteurs de fichiers jusqu'à 1 048 576 (plafonnés
// par la limite dure), comme batocera-wine.
func raiseNofileLimit() {
	var rl syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl) != nil {
		return
	}
	target := uint64(1048576)
	if rl.Max != ^uint64(0) && rl.Max < target {
		target = rl.Max
	}
	rl.Cur = target
	_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rl)
}

// initConfig : dossiers, option FUSE et propriétaire de Wine.
func initConfig() {
	savesDir = envOr("WSQUASHFS_SAVES_DIR", filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/saves"))
	cacheDir = envOr("WSQUASHFS_CACHE", filepath.Join(os.Getenv("HOME"), ".cache/wsquashfs"))

	runAsRoot = os.Geteuid() == 0
	if runAsRoot {
		allowOtherOpt = "allow_other"
	}
	targetUser = os.Getenv("SUDO_USER")

	wineUID = os.Geteuid()
	wineGID = os.Getegid()
	if dropsToTargetUser() {
		if u, err := user.Lookup(targetUser); err == nil {
			wineUID, _ = strconv.Atoi(u.Uid)
			wineGID, _ = strconv.Atoi(u.Gid)
		}
	}
	overlaySquashOp = fmt.Sprintf("squash_to_uid=%d,squash_to_gid=%d", wineUID, wineGID)

	overlayPref = envOr("WSQUASHFS_OVERLAY", "auto")
	switch overlayPref {
	case "auto", "kernel", "fuse":
	default:
		die("WSQUASHFS_OVERLAY=" + overlayPref + " : valeurs possibles auto, kernel, fuse")
	}
	if p, err := os.Executable(); err == nil {
		selfPath = p
	}
	sysroot = os.Getenv("WSQUASHFS_SYSROOT")
}

// envOr : variable d'environnement, ou sa valeur par défaut si elle est vide.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// dropsToTargetUser : root pour le compte d'un utilisateur (sudo), Wine doit
// alors être lancé en targetUser.
func dropsToTargetUser() bool {
	return runAsRoot && targetUser != ""
}

// detectEnv : Docker ou Physical (d'après /.dockerenv et le cgroup).
func detectEnv() string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "Docker"
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		if strings.Contains(s, "docker") || strings.Contains(s, "lxc") {
			return "Docker"
		}
	}
	return "Physical"
}

// fixOwnership : rend un dossier de cache à l'utilisateur de Wine (un dossier
// créé par un autre UID bloque mkdir/rm pour tous les jeux). Jamais de chown
// -R : il traverserait les overlays encore montés.
func fixOwnership(path string) {
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	if int(st.Sys().(*syscall.Stat_t).Uid) == wineUID {
		return
	}
	chownTree(path, wineUID, wineGID)
}

// chownTree : chown -h récursif, limité au système de fichiers de root (-xdev)
// et aux entrées qui n'appartiennent pas déjà à uid. sudo -n hors root.
func chownTree(root string, uid, gid int) {
	st, err := os.Stat(root)
	if err != nil {
		return
	}
	dev := st.Sys().(*syscall.Stat_t).Dev
	walkTree(root, dev, func(p string, info os.FileInfo) {
		s := info.Sys().(*syscall.Stat_t)
		if int(s.Uid) == uid {
			return
		}
		if os.Geteuid() == 0 {
			_ = os.Lchown(p, uid, gid)
		} else {
			_ = runQuiet("sudo", "-n", "chown", "-h", fmt.Sprintf("%d:%d", uid, gid), p)
		}
	})
}

// chownToTarget : lancé en root, rend le chemin à targetUser (jamais à travers
// un montage FUSE, chaque chown y déclencherait un copy-up).
func chownToTarget(path string) {
	if !dropsToTargetUser() {
		return
	}
	if _, err := os.Stat(path); err != nil {
		return
	}
	if isMountPoint(path) {
		return
	}
	u, err := user.Lookup(targetUser)
	if err != nil {
		return
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	walkTree(path, st.Sys().(*syscall.Stat_t).Dev, func(p string, info os.FileInfo) {
		s := info.Sys().(*syscall.Stat_t)
		if int(s.Uid) != uid {
			_ = os.Lchown(p, uid, gid)
		}
	})
}

func runQuiet(name string, args ...string) error {
	return execQuiet(name, args...)
}
