package main

// Montages FUSE, overlay (noyau dans un namespace, ou fuse-overlayfs), cache
// et verrous par jeu (script bash, sections 4).
//
// Les démons FUSE ne reçoivent jamais le verrou du jeu : os.OpenFile ouvre en
// O_CLOEXEC, et seul le processus de jeu reçoit explicitement le descripteur
// (ExtraFiles), comme le faisait without_lock en bash.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// fuseUmount : démontage FUSE (fusermount, puis umount).
func fuseUmount(p string) {
	if execQuiet("fusermount", "-u", p) == nil {
		return
	}
	if execQuiet("fusermount3", "-u", p) == nil {
		return
	}
	_ = execQuiet("umount", p)
}

// umountRetry : démontage de fin de partie. Juste après wineserver -k, les
// processus tiennent encore des fichiers ouverts ; quelques essais, puis
// démontage différé en dernier recours.
func umountRetry(p string) {
	for i := 0; i < 5; i++ {
		if !isMountPoint(p) {
			return
		}
		fuseUmount(p)
		if !isMountPoint(p) {
			return
		}
		time.Sleep(time.Second)
	}
	if execQuiet("fusermount3", "-uz", p) == nil || execQuiet("fusermount", "-uz", p) == nil ||
		execQuiet("umount", "-l", p) == nil {
		return
	}
}

// mountSquashfs : image montée par le lecteur intégré (imagefs.go),
// allow_other en root.
func mountSquashfs(image, point string) bool {
	if conf.allowOtherOpt != "" {
		return serveImageBg(image, point, conf.allowOtherOpt)
	}
	return serveImageBg(image, point)
}

// mountOverlay : overlay fuse-overlayfs, image (lowerdir) et sauvegardes
// (upperdir). redirect_dir=on cause des ESTALE avec fuse-overlayfs : jamais.
// Virgule et deux-points séparent les options et les couches : un chemin qui
// en contient (nom du jeu) est refusé ; le lancement passe alors en copie.
func mountOverlay(lower string) bool {
	if lower == "" {
		lower = launch.squashfsPoint
	}
	for _, p := range []string{lower, launch.savePoint, launch.workPoint} {
		if strings.ContainsAny(p, ",:") {
			warn("virgule ou deux-points dans " + p + " : fuse-overlayfs impossible")
			return false
		}
	}
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s,%s", lower, launch.savePoint, launch.workPoint, conf.overlaySquashOp)
	if conf.allowOtherOpt != "" {
		opts += "," + conf.allowOtherOpt
	}
	return execQuiet("fuse-overlayfs", "-o", opts, launch.winePoint) == nil
}

// resetWorkdir : vide le workdir. L'overlay noyau y laisse work/ en mode 000.
func resetWorkdir(d string) {
	_ = filepath.Walk(d, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			_ = os.Chmod(p, info.Mode()|0o700)
		}
		return nil
	})
	_ = os.RemoveAll(d)
	_ = os.MkdirAll(d, 0o755)
}

// reloadOverlay : remonte l'overlay, qui garde sa vue des dossiers en mémoire.
func reloadOverlay(msg string) {
	if launch.overlayKind == "kernel" {
		if execQuiet("umount", launch.winePoint) != nil {
			_ = execQuiet("umount", "-l", launch.winePoint)
		}
		if msg := mountKernelOverlayLayer(); msg != "" {
			die("échec du rechargement de l'overlay " + msg)
		}
		return
	}
	fuseUmount(launch.winePoint)
	resetWorkdir(launch.workPoint)
	chownToTarget(launch.workPoint)
	if !mountOverlay("") {
		die("échec du rechargement de l'overlay " + msg)
	}
}

// ── Overlay noyau dans un namespace utilisateur (17/10) ────────────────

// kernelOverlayOpts : redirect_dir=nofollow (01/11 : redirect_dir=off est
// refusé avec userxattr sur Linux 6.18).
const kernelOverlayOpts = "userxattr,redirect_dir=nofollow"

// kernelOverlayUnavailable : raison pour laquelle l'overlay noyau ne peut pas
// être essayé, "" sinon.
func kernelOverlayUnavailable() string {
	if conf.overlayPref == "fuse" {
		return "WSQUASHFS_OVERLAY=fuse"
	}
	if conf.runAsRoot {
		return "lancé en root"
	}
	for _, tool := range []string{"unshare", "setpriv", "mount", "umount", "mountpoint", "getfattr", "setfattr"} {
		if _, err := exec.LookPath(tool); err != nil {
			return tool + " absent"
		}
	}
	if out, _ := exec.Command("unshare", "--help").CombinedOutput(); !strings.Contains(string(out), "--map-current-user") {
		return "unshare trop ancien (util-linux 2.38 requis)"
	}
	var rel syscall.Utsname
	if syscall.Uname(&rel) == nil {
		release := charsToString(rel.Release[:])
		parts := strings.SplitN(release, ".", 3)
		if len(parts) >= 2 {
			major, _ := strconv.Atoi(parts[0])
			minor, _ := strconv.Atoi(strings.TrimRightFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
			if major < 5 || (major == 5 && minor < 11) {
				return "noyau " + release + " (5.11 requis)"
			}
		}
	}
	if v := readTrim("/proc/sys/kernel/unprivileged_userns_clone"); v == "0" || readTrim("/proc/sys/user/max_user_namespaces") == "0" {
		return "namespaces utilisateur désactivés (sysctl)"
	}
	if f, err := os.OpenFile("/dev/fuse", os.O_RDWR, 0); err != nil {
		return "/dev/fuse inaccessible"
	} else {
		f.Close()
	}
	if casefoldCapable(conf.savesDir) {
		return "sauvegardes sur un système de fichiers à casse ignorée possible (casefold, SteamOS)"
	}
	return ""
}

// fsCasefoldFlag : FS_CASEFOLD_FL (chattr +F), dossier insensible à la casse.
const fsCasefoldFlag = 0x40000000

// casefoldCapable : le système de fichiers de dir (ou de son premier parent
// existant) accepte les dossiers insensibles à la casse (ext4 avec casefold :
// /home de SteamOS). Wine y marque drive_c à la création du prefix, l'overlay
// noyau transmet la demande au dossier des sauvegardes, puis refuse toute
// recherche dessous (« parent wrong casefold », ESTALE : Steam Deck, 16/11).
// fuse-overlayfs ne transmet pas ce marqueur : il reste utilisable.
func casefoldCapable(dir string) bool {
	for {
		if _, err := os.Stat(dir); err == nil || dir == "/" || dir == "." {
			break
		}
		dir = filepath.Dir(dir)
	}
	tmp, err := os.MkdirTemp(dir, ".wsquashfs-casefold.")
	if err != nil {
		return false
	}
	defer os.Remove(tmp)
	f, err := os.Open(tmp)
	if err != nil {
		return false
	}
	defer f.Close()
	flags, err := unix.IoctlGetInt(int(f.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil {
		return false
	}
	if flags&fsCasefoldFlag != 0 {
		return true // hérité du parent
	}
	return unix.IoctlSetPointerInt(int(f.Fd()), unix.FS_IOC_SETFLAGS, flags|fsCasefoldFlag) == nil
}

// readTrim : contenu d'un petit fichier, sans fin de ligne ("" si absent).
func readTrim(p string) string {
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// charsToString : chaîne C (octets nuls ignorés). Utsname contient des int8
// sur x86-64 et des uint8 sur arm64.
func charsToString[T int8 | uint8](b []T) string {
	var sb strings.Builder
	for _, c := range b {
		if c == 0 {
			break
		}
		sb.WriteByte(byte(c))
	}
	return sb.String()
}
