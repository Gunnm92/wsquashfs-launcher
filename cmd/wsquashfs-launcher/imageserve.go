package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"wsquashfs-launcher/internal/imagefs"
)

// serveImage : --serve-image IMAGE POINT [uid N] [gid N] [allow_other]. Sert
// l'image jusqu'au démontage ; SIGTERM/SIGINT démontent proprement.
func serveImage(args []string) int {
	o, err := imagefs.ParseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "--serve-image : "+err.Error())
		return 2
	}
	if err := imagefs.Serve(o); err != nil {
		fmt.Fprintln(os.Stderr, "--serve-image : "+err.Error())
		return 1
	}
	return 0
}

// imageServeCmd : commande du serveur d'image (le lanceur lui-même).
func imageServeCmd(image, point string, extra ...string) *exec.Cmd {
	args := append([]string{"--serve-image", image, point}, extra...)
	return exec.Command(conf.selfPath, args...)
}

// serveImageBg : monte l'image en arrière-plan (comme squashfuse, qui rend la
// main une fois monté). Le serveur survit au lanceur jusqu'au démontage.
func serveImageBg(image, point string, extra ...string) bool {
	cmd := imageServeCmd(image, point, extra...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return false
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	return waitMounted(point, exited)
}

// waitMounted : attend le montage (10 s au plus) ou la fin du serveur.
func waitMounted(point string, exited <-chan struct{}) bool {
	for i := 0; i < 100; i++ {
		if isMountPoint(point) {
			return true
		}
		select {
		case <-exited:
			return isMountPoint(point)
		case <-time.After(100 * time.Millisecond):
		}
	}
	return isMountPoint(point)
}

// fusermountPath : outil setuid de montage FUSE hors namespace ("" si absent).
func fusermountPath() string {
	for _, t := range []string{"fusermount3", "fusermount"} {
		if p, err := exec.LookPath(t); err == nil {
			return p
		}
	}
	return ""
}
