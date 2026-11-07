package main

// Parcours de dossiers et commandes utilitaires (find -xdev, montages).

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// walkTree : parcourt root (sans suivre les liens, comme find -xdev) et
// appelle fn pour chaque entrée du même système de fichiers que dev.
func walkTree(root string, dev uint64, fn func(path string, info os.FileInfo)) {
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Sys().(*syscall.Stat_t).Dev != dev {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fn(p, info)
		return nil
	})
}

// isMountPoint : vrai si path est un point de montage (mountpoint -q).
func isMountPoint(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return false
	}
	return st.Sys().(*syscall.Stat_t).Dev != parent.Sys().(*syscall.Stat_t).Dev
}

// mountedPaths : chemins des montages (lus dans /proc/self/mountinfo).
func mountedPaths() []string {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 4 {
			out = append(out, strings.ReplaceAll(fields[4], `\040`, " "))
		}
	}
	return out
}

// execQuiet : exécute une commande, sorties ignorées.
func execQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}

// squashfsCat : contenu d'un fichier de l'image, sans la monter (unsquashfs
// -cat). Vide si absent.
func squashfsCat(image, file string) string {
	out, err := exec.Command("unsquashfs", "-cat", image, file).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
