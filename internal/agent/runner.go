package agent

// Routes GET /v1/runner/list et GET /v1/runner/tar : lecture des runners installés.

import (
	"archive/tar"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// runnerDir : dossier demandé, seulement s'il est un runner installé.
func (s *service) runnerDir(path string) (string, int, string) {
	if !strings.HasPrefix(path, "/") {
		return "", http.StatusBadRequest, "chemin absolu requis"
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", http.StatusNotFound, "dossier introuvable"
	}
	for _, base := range s.env.ListRunners() {
		realBase, err := filepath.EvalSymlinks(base)
		if err != nil {
			continue
		}
		if within(real, realBase) {
			if !isDirPath(real) {
				return "", http.StatusNotFound, "dossier introuvable"
			}
			return real, 0, ""
		}
	}
	return "", http.StatusForbidden, "ce dossier n'est pas un runner installé sur cette machine"
}

func (s *service) runnerList(w http.ResponseWriter, r *http.Request) {
	real, code, msg := s.runnerDir(r.URL.Query().Get("path"))
	if code != 0 {
		writeDetail(w, code, msg)
		return
	}
	entries := map[string]int64{}
	_ = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == real {
			return nil
		}
		rel, _ := filepath.Rel(real, p)
		var size int64
		if info, err := d.Info(); err == nil && d.Type().IsRegular() {
			size = info.Size()
		}
		entries[rel] = size
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]any{"path": real, "entries": entries})
}

// runnerTar : tar du dossier de runner (liens symboliques conservés), en flux.
func (s *service) runnerTar(w http.ResponseWriter, r *http.Request) {
	real, code, msg := s.runnerDir(r.URL.Query().Get("path"))
	if code != 0 {
		writeDetail(w, code, msg)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.WriteHeader(http.StatusOK)
	tw := tar.NewWriter(w)
	top := filepath.Base(real)
	err := filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(real, p)
		name := top
		if rel != "." {
			name = top + "/" + filepath.ToSlash(rel)
		}
		info, err := os.Lstat(p)
		if err != nil {
			return nil
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			link, _ = os.Readlink(p)
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return nil
		}
		hdr.Name = name
		if info.IsDir() {
			hdr.Name += "/"
		}
		if !info.Mode().IsRegular() {
			return tw.WriteHeader(hdr)
		}
		// Fichier ouvert avant son en-tête : illisible, il est omis sans
		// fausser la suite de l'archive.
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		// Taille annoncée dans l'en-tête : un fichier raccourci entre-temps
		// rendrait toute la suite illisible.
		_, err = io.CopyN(tw, f, hdr.Size)
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	if err != nil {
		// Le code 200 est déjà parti : la connexion est coupée, pour que le
		// client voie un échec et non une archive tronquée.
		panic(http.ErrAbortHandler)
	}
}
