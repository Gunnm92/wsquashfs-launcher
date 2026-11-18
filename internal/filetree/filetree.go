// Package filetree : copie de fichiers et recherche dans des arborescences,
// partagées par l'installeur et le lanceur.
package filetree

import (
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
