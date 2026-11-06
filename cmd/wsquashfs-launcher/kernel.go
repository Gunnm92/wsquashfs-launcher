package main

// Overlay du noyau dans un namespace utilisateur (17/10), script bash, section 4.
// L'hôte prépare les dossiers, puis se relance dans un namespace
// (unshare --map-current-user) : le namespace monte l'image (squashfuse_ll),
// puis l'overlay, puis lance le jeu. Le statut revient à l'hôte par un
// fichier NS_DIR/status.

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

// nsCommand : unshare vers un namespace utilisateur, mapping de l'UID courant.
func nsCommand(args ...string) *exec.Cmd {
	full := append([]string{"--user", "--map-current-user", "--mount", "--keep-caps", "--"}, args...)
	return exec.Command("unshare", full...)
}

// kernelOverlayProbe : monte un overlay de test à côté des sauvegardes (le
// système de fichiers de l'upperdir compte). "" si réussi, sinon la cause.
func kernelOverlayProbe() string {
	d := savesDir
	for {
		if _, err := os.Stat(d); err == nil {
			break
		}
		d = filepath.Dir(d)
	}
	tmp, err := os.MkdirTemp(d, ".wsquashfs-probe.")
	if err != nil {
		return d + " : écriture impossible"
	}
	defer os.RemoveAll(tmp)
	script := `cd "$1" && mkdir l u w m && mount -t overlay overlay -o "lowerdir=l,upperdir=u,workdir=w,$2" m`
	cmd := nsCommand("sh", "-c", script, "_", tmp, kernelOverlayOpts)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(string(out))
	if i := strings.LastIndex(text, ": "); i >= 0 {
		text = text[i+2:]
	}
	if j := strings.Index(text, "\n"); j >= 0 {
		text = text[:j]
	}
	return text
}

// savesToKernelFormat : sauvegardes écrites par fuse-overlayfs → format de
// l'overlay noyau (dossiers opaques, fichiers supprimés = périphériques
// caractère 0/0). Faux si une conversion échoue.
func savesToKernelFormat(upper string) bool {
	var whiteouts, opaqueWh []string
	_ = filepath.Walk(upper, func(p string, info os.FileInfo, err error) error {
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		base := filepath.Base(p)
		if base == ".wh..wh..opq" {
			opaqueWh = append(opaqueWh, p)
		} else if strings.HasPrefix(base, ".wh.") && !strings.HasPrefix(base, ".wh..wh..") {
			whiteouts = append(whiteouts, p)
		}
		return nil
	})
	for _, f := range opaqueWh {
		if execQuiet("setfattr", "-n", "user.overlay.opaque", "-v", "y", filepath.Dir(f)) != nil {
			return false
		}
		_ = os.Remove(f)
	}
	out, _ := exec.Command("getfattr", "-R", "-h", "--absolute-names", "-n", "user.fuseoverlayfs.opaque", "--", upper).Output()
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "# file: ") {
			continue
		}
		d := bashPrintfB(strings.TrimPrefix(line, "# file: "))
		if execQuiet("setfattr", "-n", "user.overlay.opaque", "-v", "y", d) != nil {
			return false
		}
	}
	for _, f := range whiteouts {
		d := filepath.Dir(f)
		name := strings.TrimPrefix(filepath.Base(f), ".wh.")
		target := filepath.Join(d, name)
		if _, err := os.Lstat(target); err != nil {
			if execQuiet("mknod", target, "c", "0", "0") != nil {
				return false
			}
		}
		_ = os.Remove(f)
	}
	return true
}

// bashPrintfB : printf '%b' (séquences \\ et \ooo, comme getfattr les écrit).
func bashPrintfB(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			sb.WriteByte(s[i])
			continue
		}
		if s[i+1] == '\\' {
			sb.WriteByte('\\')
			i++
			continue
		}
		if i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				sb.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// runInKernelOverlay : hôte, relance le lanceur dans un namespace. Ne revient
// pas si le jeu y a été lancé ; revient pour le repli sur fuse-overlayfs.
func runInKernelOverlay() {
	if action == "mount" || folderOverlay {
		return
	}
	if reason := kernelOverlayUnavailable(); reason != "" {
		if overlayPref == "kernel" {
			die("overlay noyau impossible : " + reason)
		}
		if overlayPref != "fuse" {
			fmt.Println("Overlay noyau : indisponible, " + reason)
		}
		return
	}
	// Essai à vide : un refus (conteneur, AppArmor) donne sa cause sans message.
	var reason string
	if out, err := nsCommand("true").CombinedOutput(); err != nil {
		reason = "namespace utilisateur refusé (" + lastField(string(out)) + ")"
	} else if !savesToKernelFormat(savePoint) {
		reason = "conversion des sauvegardes impossible"
	} else {
		rc, failReason, ok := stageInNamespace()
		if ok {
			exitNow(rc)
		}
		reason = failReason
	}
	if overlayPref == "kernel" {
		die("overlay noyau impossible : " + reason)
	}
	fmt.Println("Overlay noyau : échec, " + reason)
}

// lastField : dernier élément après « : » d'un message d'erreur, première ligne.
func lastField(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	if j := strings.Index(s, "\n"); j >= 0 {
		s = s[:j]
	}
	return s
}

// stageInNamespace : prépare le dossier d'échange, lance le lanceur dans le
// namespace (fd du verrou transmis), attend. ok vrai si le jeu y a tourné.
func stageInNamespace() (int, string, bool) {
	nsDir = filepath.Join(cacheDir, "ns", strconv.Itoa(os.Getpid()))
	resetWorkdir(nsDir)
	_ = os.WriteFile(filepath.Join(nsDir, "game"), []byte(gameName+"\n"), 0o644)
	_ = os.Symlink(squashfsPoint, filepath.Join(nsDir, "lower"))
	_ = os.Symlink(savePoint, filepath.Join(nsDir, "upper"))
	_ = os.Symlink(workPoint, filepath.Join(nsDir, "work"))

	cmd := exec.Command("unshare", "--user", "--map-current-user", "--mount", "--keep-caps", "--", selfPath)
	cmd.Args = append(cmd.Args, origArgs...)
	cmd.Env = append(os.Environ(),
		"WSQUASHFS_NS_STAGE=1",
		"WSQUASHFS_NS_DIR="+nsDir,
		"WSQUASHFS_NS_LOCK_FD=3",
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if lockFile != nil {
		cmd.ExtraFiles = []*os.File{lockFile}
	}
	if err := cmd.Start(); err != nil {
		return 0, "lancement impossible", false
	}
	nsPID = cmd.Process.Pid
	err := cmd.Wait()
	nsPID = 0
	rc := 0
	if ee, ok := err.(*exec.ExitError); ok {
		rc = ee.ExitCode()
	}
	status := strings.TrimSpace(readTrim(filepath.Join(nsDir, "status")))
	_ = os.RemoveAll(nsDir)
	nsDir = ""
	if status == "ready" {
		return rc, "", true
	}
	if status == "" {
		status = "namespace utilisateur refusé"
	}
	// Montage noyau interrompu : work/ (mode 000) resté dans le workdir.
	resetWorkdir(workPoint)
	chownToTarget(workPoint)
	return rc, status, false
}

// mountKernelOverlayLayer : monte l'overlay noyau, chemins relatifs au
// dossier d'échange.
func mountKernelOverlayLayer() string {
	cmd := exec.Command("mount", "-t", "overlay", "overlay",
		"-o", "lowerdir=lower,upperdir=upper,workdir=work,"+kernelOverlayOpts, winePoint)
	cmd.Dir = nsDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return ""
	}
	if m := lastField(string(out)); m != "" {
		return m
	}
	return "montage refusé"
}

// useKernelOverlay : dans le namespace, monte l'image puis l'overlay. Le statut
// (ready ou la cause) va dans NS_DIR/status pour l'hôte.
func useKernelOverlay() {
	writeStatus := func(s string) {
		_ = os.WriteFile(filepath.Join(nsDir, "status"), []byte(s+"\n"), 0o644)
	}
	_ = os.WriteFile(filepath.Join(nsDir, "pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	sq := exec.Command("squashfuse_ll", "-f", "-o", fmt.Sprintf("uid=%d,gid=%d", wineUID, wineGID), wsquashfsFile, squashfsPoint)
	sq.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	_ = sq.Start()
	for i := 0; i < 100; i++ {
		if isMountPoint(squashfsPoint) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !isMountPoint(squashfsPoint) {
		writeStatus("squashfuse_ll impossible dans le namespace")
		exitNow(1)
	}
	if msg := mountKernelOverlayLayer(); msg != "" {
		writeStatus("overlay refusé (" + msg + ")")
		exitNow(1)
	}
	writeStatus("ready")
	mountMode = "overlay"
	overlayKind = "kernel"
	mounted = true
	detectImagePrefix()
	fmt.Println("Mode          : overlay noyau (squashfuse + overlayfs, namespace utilisateur)")
}

// nsLauncherPID : PID du lanceur d'un jeu dans son namespace, pour --umount.
func nsLauncherPID(game string) (int, bool) {
	files, _ := filepath.Glob(filepath.Join(cacheDir, "ns", "*", "game"))
	for _, f := range files {
		if strings.TrimSpace(readTrim(f)) != game {
			continue
		}
		dir := filepath.Dir(f)
		pid, err := strconv.Atoi(readTrim(filepath.Join(dir, "pid")))
		if err != nil {
			continue
		}
		env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err != nil {
			continue
		}
		if strings.Contains(string(env), "WSQUASHFS_NS_DIR="+dir+"\x00") {
			return pid, true
		}
	}
	return 0, false
}
