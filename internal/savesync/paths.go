package savesync

// Chemins et petits utilitaires de fichiers du périmètre de synchro.

import (
	"os"
	"path/filepath"
	"strings"
)

// saveProfileDirs : dossiers du profil Windows où les jeux écrivent leurs sauvegardes de partie.
var saveProfileDirs = []string{"Documents", "Saved Games", "AppData/Roaming", "AppData/LocalLow", "AppData/Local",
	"My Documents", "Application Data", "Local Settings/Application Data"}

// isCacheDirName : dossiers de caches exclus des sauvegardes.
func isCacheDirName(n string) bool {
	switch n {
	case "Temp", "NVIDIA", "D3DSCache", "DXCache", "INetCache", "CrashDumps":
		return true
	}
	return strings.Contains(strings.ToLower(n), "shadercache")
}

// StateRoot : état des synchros, par machine cliente (dossier caché de SavesDir).
func (s *Syncer) StateRoot() string {
	return filepath.Join(s.env.SavesDir, ".sync")
}

// copyUsersStash : drive_c/users d'une copie de travail (mode copy), mis de côté pendant que la
// copie est supprimée ou refaite.
func (s *Syncer) copyUsersStash(name string) string {
	if s.env.CopyUsersStash != nil {
		return s.env.CopyUsersStash(name)
	}
	return filepath.Join(s.env.CacheDir, "copy-users", name)
}

// gameNameOf : nom du jeu d'après une image, un dossier ou un nom. Un dossier Jeu.pc a le nom de
// l'image qu'il donnera (Jeu.wsquashfs).
func gameNameOf(p string) string {
	n := filepath.Base(strings.TrimSuffix(p, "/"))
	switch {
	case strings.HasSuffix(n, ".wsquashfs"):
		return strings.TrimSuffix(n, ".wsquashfs")
	case strings.HasSuffix(strings.ToLower(n), ".pc"):
		return n[:len(n)-3]
	}
	return n
}

// isDirPath : chemin existant et dossier.
func isDirPath(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// isFile : fichier ordinaire (test -f).
func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// visibleDirs : sous-dossiers non cachés de d (les noms en point sont ignorés, comme .work et .images).
func visibleDirs(d string) []string {
	entries, _ := os.ReadDir(d)
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// removeEmptyDirs : supprime dir, puis ses parents devenus vides, jusqu'à (exclu) stop. Un dossier
// non vide arrête la remontée.
func removeEmptyDirs(dir, stop string) {
	for dir != stop && strings.HasPrefix(dir, stop+string(filepath.Separator)) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// configHome : dossier de configuration de l'utilisateur ($XDG_CONFIG_HOME, sinon ~/.config).
func configHome() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return base
}
