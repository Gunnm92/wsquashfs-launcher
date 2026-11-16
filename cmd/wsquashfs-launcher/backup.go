package main

// Archives des sauvegardes mises de côté (--erase, version perdante d'une synchro) : dossier des
// archives, archivage, et retrait des plus anciennes par --clean.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// backupDir : archives de --erase.
func backupDir() string {
	return envOr("WSQUASHFS_BACKUP_DIR", filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/backups"))
}

// backupsKept : archives gardées par jeu ; --clean retire les plus anciennes.
const backupsKept = 3

var backupNameRe = regexp.MustCompile(`^(.*)-(\d{8}-\d{6})(-\d+)?\.tar\.(zst|gz)$`)

// pruneBackups : ne garde que les backupsKept dernières archives de chaque
// jeu, et retire les archives inachevées (.part). Les autres fichiers du
// dossier ne sont pas touchés.
func pruneBackups() (n int, size int64) {
	dir := backupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	byGame := map[string][]string{}
	var doomed []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		switch m := backupNameRe.FindStringSubmatch(e.Name()); {
		case m != nil:
			byGame[m[1]] = append(byGame[m[1]], e.Name())
		case strings.HasSuffix(e.Name(), ".part"):
			doomed = append(doomed, e.Name())
		}
	}
	for _, names := range byGame {
		// L'horodatage du nom donne l'ordre ; à date égale, le suffixe -N.
		sort.Slice(names, func(i, j int) bool {
			a, b := backupNameRe.FindStringSubmatch(names[i]), backupNameRe.FindStringSubmatch(names[j])
			if a[2] != b[2] {
				return a[2] > b[2]
			}
			return len(a[3]) > len(b[3]) || (len(a[3]) == len(b[3]) && a[3] > b[3])
		})
		if len(names) > backupsKept {
			doomed = append(doomed, names[backupsKept:]...)
		}
	}
	sort.Strings(doomed)
	for _, name := range doomed {
		f := filepath.Join(dir, name)
		if st, err := os.Stat(f); err == nil && os.Remove(f) == nil {
			n++
			size += st.Size()
		}
	}
	if n > 0 {
		uiStatusLine(stOK, fmt.Sprintf("Anciennes archives supprimées : %d (%s)", n, dir))
	}
	return n, size
}

// archiveUsers : archive <parent>/<member> dans backupDir, sous le nom
// <jeu>-AAAAMMJJ-HHMMSS.tar.zst (tar.gz sans zstd). L'archive n'apparaît
// sous son nom qu'une fois complète.
func archiveUsers(name, parent, member string) (string, error) {
	dir := backupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext, flag := "tar.zst", "--zstd"
	if _, err := exec.LookPath("zstd"); err != nil {
		ext, flag = "tar.gz", "-z"
	}
	base := filepath.Join(dir, name+"-"+time.Now().Format("20060102-150405"))
	out := base + "." + ext
	for i := 2; pathExists(out); i++ {
		out = fmt.Sprintf("%s-%d.%s", base, i, ext)
	}
	part := out + ".part"
	// « -- » : un membre qui commence par un tiret (nom de jeu, de dossier) n'est pas une option.
	if err := execQuiet("tar", flag, "-cf", part, "-C", parent, "--", member); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, out); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	chownToTarget(dir)
	return out, nil
}
