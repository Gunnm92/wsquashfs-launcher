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

// doUmount : démontage simple.
func doUmount(p string) {
	fuseUmount(p)
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

// mountSquashfs : squashfuse, allow_other en root.
func mountSquashfs(image, point string) bool {
	args := []string{}
	if allowOtherOpt != "" {
		args = append(args, "-o", allowOtherOpt)
	}
	args = append(args, image, point)
	return execDiscard("squashfuse", args...) == nil
}

// mountOverlay : overlay fuse-overlayfs, image (lowerdir) et sauvegardes
// (upperdir). redirect_dir=on cause des ESTALE avec fuse-overlayfs : jamais.
func mountOverlay(lower string) bool {
	if lower == "" {
		lower = squashfsPoint
	}
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s,%s", lower, savePoint, workPoint, overlaySquashOp)
	if allowOtherOpt != "" {
		opts += "," + allowOtherOpt
	}
	return execDiscard("fuse-overlayfs", "-o", opts, winePoint) == nil
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
	if overlayKind == "kernel" {
		if execQuiet("umount", winePoint) != nil {
			_ = execQuiet("umount", "-l", winePoint)
		}
		if msg := mountKernelOverlayLayer(); msg != "" {
			die("échec du rechargement de l'overlay " + msg)
		}
		return
	}
	doUmount(winePoint)
	resetWorkdir(workPoint)
	chownToTarget(workPoint)
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
	if overlayPref == "fuse" {
		return "WSQUASHFS_OVERLAY=fuse"
	}
	if runAsRoot {
		return "lancé en root"
	}
	for _, tool := range []string{"unshare", "setpriv", "squashfuse_ll", "mount", "umount", "mountpoint", "getfattr", "setfattr"} {
		if _, err := exec.LookPath(tool); err != nil {
			return tool + " absent"
		}
	}
	if out, _ := exec.Command("squashfuse_ll", "-h").CombinedOutput(); !strings.Contains(string(out), "uid=N") {
		return "squashfuse_ll sans option uid= (squashfuse 0.5 requis)"
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
	return ""
}

// readTrim : contenu d'un petit fichier, sans fin de ligne ("" si absent).
func readTrim(p string) string {
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// charsToString : chaîne C (octets nuls ignorés).
func charsToString(b []int8) string {
	var sb strings.Builder
	for _, c := range b {
		if c == 0 {
			break
		}
		sb.WriteByte(byte(c))
	}
	return sb.String()
}
