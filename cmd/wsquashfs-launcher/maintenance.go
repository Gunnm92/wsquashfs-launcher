package main

// Aiguillage des commandes de maintenance (--clean, --list, --info, --pack, --save, --update...)
// et petits utilitaires communs (taille lisible, chemins, tri, terminal).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// maintenanceMain : dispatch des commandes de maintenance.
func maintenanceMain() int {
	switch launch.action {
	case "clean":
		cmdClean()
	case "clear":
		return cmdClear(launch.cmdArgs)
	case "erase":
		return cmdErase(launch.cmdArgs)
	case "umount":
		if len(launch.cmdArgs) > 0 {
			umountGame(gameNameOf(launch.cmdArgs[0]))
			return 0
		}
		entries, _ := os.ReadDir(conf.cacheDir + "/wine")
		for _, e := range entries {
			if e.IsDir() {
				umountGame(e.Name())
			}
		}
		unmountAllStale()
	case "pack":
		return packPrefix(launch.cmdArgs)
	case "install":
		return installGame(launch.cmdArgs)
	case "info":
		return showInfo(launch.cmdArgs)
	case "show":
		return cmdShow(launch.cmdArgs)
	case "save":
		return cmdSave(launch.cmdArgs)
	case "saves":
		return cmdSaves()
	case "list":
		cmdList()
	case "doctor":
		return cmdDoctorMain(launch.cmdArgs)
	case "agent":
		return cmdAgentMain(launch.cmdArgs)
	case "pair":
		return cmdPair(launch.cmdArgs)
	case "sync":
		return syncer().Run(launch.cmdArgs)
	case "tray":
		return cmdTray(launch.cmdArgs)
	case "trial":
		return cmdTrialMain(launch.cmdArgs)
	case "update":
		return cmdUpdate()
	}
	return 0
}

// duHuman : taille lisible (du -sh) ; vide si erreur.
func duHuman(p string) string {
	// du sort une taille même en erreur (sous-dossier illisible) : code ignoré.
	out, _ := exec.Command("du", "-sh", p).Output()
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// humanBytes : numfmt --to=iec --suffix=o.
func humanBytes(n int64) string {
	out, err := exec.Command("numfmt", "--to=iec", "--suffix=o", strconv.FormatInt(n, 10)).Output()
	if err != nil {
		return strconv.FormatInt(n, 10) + "o"
	}
	return strings.TrimSpace(string(out))
}

// isDirPath : chemin existant et dossier.
func isDirPath(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// isTerminal : entrée standard sur un terminal (test -t 0 : ioctl TCGETS, car
// /dev/null est aussi un périphérique caractère).
func isTerminal() bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

// uniqueSorted : éléments distincts d'une liste triée.
func uniqueSorted(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// mustAbs : chemin absolu (ou tel quel en cas d'erreur).
func mustAbs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
