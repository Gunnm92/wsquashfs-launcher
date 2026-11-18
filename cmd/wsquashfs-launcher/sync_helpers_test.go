package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wsquashfs-launcher/internal/savesync"
)

// syncTestTime : date fixe des fichiers de test.
var syncTestTime = time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)

// syncTestPut : écrit un fichier avec une date fixe.
func syncTestPut(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// syncTestPopulate : users/ d'une machine avec le profil donné, les sauvegardes du périmètre et
// des fichiers hors périmètre (cache, Public, .wh., dossier non sauvegarde, lien).
func syncTestPopulate(t *testing.T, users, profile string) {
	t.Helper()
	p := filepath.Join(users, profile)
	syncTestPut(t, filepath.Join(p, "Documents", "Game", "save1.sav"), "sauvegarde", syncTestTime)
	syncTestPut(t, filepath.Join(p, "AppData", "Local", "Game", "cfg.ini"), "cfg", syncTestTime.Add(time.Hour))
	syncTestPut(t, filepath.Join(p, "Temp", "x"), "temp", syncTestTime)
	syncTestPut(t, filepath.Join(p, "AppData", "Local", "Temp", "cache.tmp"), "tmp", syncTestTime)
	syncTestPut(t, filepath.Join(p, "Documents", "shadercache_x", "s.bin"), "shader", syncTestTime)
	syncTestPut(t, filepath.Join(p, "Documents", ".wh.x"), "wh", syncTestTime)
	syncTestPut(t, filepath.Join(p, "Pictures", "p.png"), "png", syncTestTime)
	syncTestPut(t, filepath.Join(users, "Public", "Documents", "pub.sav"), "public", syncTestTime)
	if err := os.Symlink(filepath.Join(p, "Pictures", "p.png"), filepath.Join(p, "Documents", "lien.sav")); err != nil {
		t.Fatal(err)
	}
}

// syncTestTree : chemin -> contenu et date, pour vérifier qu'un dossier n'a pas changé.
func syncTestTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(root, func(f string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, f)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(f)
			out[rel] = "->" + target
		case info.IsDir():
			out[rel] = "dir"
		default:
			data, _ := os.ReadFile(f)
			out[rel] = string(data) + "@" + info.ModTime().String()
		}
		return nil
	})
	return out
}

// writeTree : crée les fichiers (chemin relatif → contenu) sous root, avec une date fixe par fichier.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	i := 0
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(1_700_000_000+int64(i)*60, 0)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		i++
	}
}

// plainArchive : archive d'un dossier simple, en mémoire.
func plainArchive(t *testing.T, root string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tg := savesync.Target{Dir: root, Plain: true}
	if _, err := tg.WriteArchive(&buf, tg.Files()); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// saveFingerprint : empreinte du périmètre d'un users/ de jeu.
func saveFingerprint(users string, files []savesync.File) string {
	return syncer().GameTarget("", users).Fingerprint(files)
}

// writeSaveArchive : archive tar.gz du périmètre d'un users/ de jeu ; profil réel s'il est unique.
func writeSaveArchive(w io.Writer, users string, files []savesync.File) (string, error) {
	return syncer().GameTarget("", users).WriteArchive(w, files)
}

// edenFixture : Eden dans un HOME temporaire : deux profils (le second a plus de jeux), un nom de jeu
// dans le cache de la liste. Retourne le dossier du profil retenu.
func edenFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("WSQUASHFS_EDEN_DIR", "")
	root := filepath.Join(home, ".local", "share", "eden", "nand", "user", "save", "0000000000000000")
	small := filepath.Join(root, "00000000000000000000000000000001")
	big := filepath.Join(root, "0000000000000000000000000000000A")
	writeTree(t, small, map[string]string{"01007EF00011E000/save.bin": "petit"})
	writeTree(t, big, map[string]string{
		"0100F2C0115B6000/progress.dat": "zelda",
		"01006A800016E000/slot0":        "mario",
	})
	if err := os.MkdirAll(filepath.Join(big, "01000000DEADBEEF"), 0o755); err != nil { // jeu sans sauvegarde
		t.Fatal(err)
	}
	cache := filepath.Join(home, ".cache", "eden", "game_list")
	writeTree(t, cache, map[string]string{"0100F2C0115B6000.appname.txt": "The Legend of Zelda: Tears of the Kingdom\n"})
	return big
}
