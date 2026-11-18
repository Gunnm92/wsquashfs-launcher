// Package filetree : copie de fichiers et recherche dans des arborescences,
// partagées par l'installeur et le lanceur.
package filetree

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CopyFile copie src dans dst avec le mode donné.
func CopyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copie de %s : %w", src, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// Find : premier chemin (entrées de dossiers et fichiers) accepté par match,
// parcouru dans chaque racine jusqu'à maxDepth niveaux (find -maxdepth).
// Équivalent du find | grep du script ; ordre lexical.
func Find(roots []string, maxDepth int, match func(path string) bool) string {
	for _, root := range roots {
		found := ""
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if rel == "." {
				return nil
			}
			if strings.Count(rel, string(os.PathSeparator))+1 > maxDepth {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if match(p) {
				found = p
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found
		}
	}
	return ""
}

// ErrHomeInaccessible : HOME absent ou illisible.
var ErrHomeInaccessible = errors.New("HOME inaccessible")

// Home : HOME avec les liens résolus.
func Home() (string, error) {
	home := os.Getenv("HOME")
	if home == "" {
		return "", errors.New("HOME absent")
	}
	return filepath.EvalSymlinks(home)
}

// StrictlyUnder : p est un sous-dossier de base (base elle-même exclue).
func StrictlyUnder(base, p string) bool {
	return p != base && within(p, base)
}

// within : p est base ou dessous.
func within(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// Ancestor : p s'il existe, sinon son plus proche parent existant ("" si aucun).
func Ancestor(p string) string {
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
}

// UnderHome : dossier prévu (qui peut ne pas encore exister). Le plus proche ancêtre existant, une
// fois les liens résolus, doit être sous HOME ; retourne le chemin avec ce préfixe réel.
// ErrHomeInaccessible si HOME ne se résout pas ; toute autre erreur est un refus du chemin.
func UnderHome(dir string) (string, error) {
	realHome, err := Home()
	if err != nil {
		return "", ErrHomeInaccessible
	}
	anc := Ancestor(dir)
	if anc == "" {
		return "", errors.New("dossier hors HOME : " + dir)
	}
	realAnc, err := filepath.EvalSymlinks(anc)
	if err != nil || !within(realAnc, realHome) {
		return "", errors.New("dossier hors HOME : " + dir)
	}
	rest, err := filepath.Rel(anc, dir)
	if err != nil {
		return "", errors.New("dossier invalide : " + dir)
	}
	return filepath.Join(realAnc, rest), nil
}
