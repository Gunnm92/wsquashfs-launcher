package main

// Installation des images .wsquashfs par wsquashfs-manager (routes /v1/games/*, agent_http.go).
// L'image est envoyée par morceaux reprenables : PUT avec offset = taille déjà reçue (fichier
// « <nom>.part »), puis POST commit qui vérifie l'empreinte et renomme. Le dossier d'installation
// est toujours sous HOME, liens symboliques résolus : l'agent n'écrit jamais ailleurs.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"wsquashfs-launcher/internal/download"
	"wsquashfs-launcher/internal/filetree"
	"wsquashfs-launcher/internal/savesync"
)

const (
	gamesDetectTTL  = 10 * time.Minute
	gamesFreeMargin = 1 << 30 // espace libre gardé après l'image : 1 Gio
	gamesExt        = ".wsquashfs"
	gamesPartExt    = ".wsquashfs.part"
	gamesPutTimeout = 10 * time.Minute // délai de lecture d'un envoi (ReadTimeout de l'agent : 60 s)
)

// gamesRealPath : chemin sans liens symboliques, ou nettoyé si la résolution échoue.
func gamesRealPath(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

// gamesDirCheck : dossier choisi (absolu, existant, sous HOME après résolution des liens).
// Retourne le chemin réel.
func gamesDirCheck(dir string) (string, int, string) {
	if !filepath.IsAbs(dir) {
		return "", http.StatusBadRequest, "chemin absolu requis"
	}
	realHome, err := filetree.Home()
	if err != nil {
		return "", http.StatusInternalServerError, "HOME inaccessible"
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", http.StatusBadRequest, "dossier introuvable : " + dir
	}
	if !isDirPath(real) {
		return "", http.StatusBadRequest, "pas un dossier : " + dir
	}
	if !filetree.StrictlyUnder(realHome, real) {
		return "", http.StatusBadRequest, "le dossier doit être un sous-dossier de HOME (" + realHome + ")"
	}
	return real, 0, ""
}

// gamesVerify : dossier prévu (qui peut ne pas encore exister), sous HOME (filetree.UnderHome) ;
// code HTTP et message si refusé.
func gamesVerify(dir string) (string, int, string) {
	real, err := filetree.UnderHome(dir)
	switch {
	case errors.Is(err, filetree.ErrHomeInaccessible):
		return "", http.StatusInternalServerError, err.Error()
	case err != nil:
		return "", http.StatusBadRequest, err.Error()
	}
	return real, 0, ""
}

// hiddenUnder : un composant de p, relatif à base, commence par « . ».
func hiddenUnder(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return true
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

// collectGameDirs : dir (profondeur depth) et ses sous-dossiers jusqu'à maxDepth. Les liens
// symboliques ne sont pas suivis (DirEntry.IsDir est faux pour un lien) ; les dossiers cachés sont ignorés.
func collectGameDirs(dir string, depth, maxDepth int, out map[string]bool) {
	out[dir] = true
	if depth >= maxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			collectGameDirs(filepath.Join(dir, e.Name()), depth+1, maxDepth, out)
		}
	}
}

// countGameImages : nombre de fichiers .wsquashfs réguliers directement dans dir.
func countGameImages(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), gamesExt) {
			n++
		}
	}
	return n
}

// detectGamesDir : dossier d'installation par défaut. Candidats : les dossiers sous home (4 niveaux
// au plus), et les dossiers de roms situés sous home (le dossier roms et 3 niveaux en dessous). Le
// candidat qui contient directement le plus d'images .wsquashfs gagne ; à égalité, celui qui finit par
// Games/non-steam, sinon le premier par ordre alphabétique. Aucune image : home/Games/non-steam.
// Les liens symboliques ne sont pas suivis (le dossier roms est résolu, puis doit être sous home) et les
// dossiers cachés sont ignorés. home et les chemins retournés sont sans liens symboliques.
func detectGamesDir(home string, roms []string) string {
	realHome := gamesRealPath(home)
	fallback := filepath.Join(realHome, "Games", "non-steam")
	dirs := map[string]bool{}
	if entries, err := os.ReadDir(realHome); err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				collectGameDirs(filepath.Join(realHome, e.Name()), 1, 4, dirs)
			}
		}
	}
	for _, root := range roms {
		real, err := filepath.EvalSymlinks(root)
		if err != nil || !filetree.StrictlyUnder(realHome, real) || hiddenUnder(realHome, real) {
			continue
		}
		collectGameDirs(real, 0, 3, dirs)
	}
	keys := make([]string, 0, len(dirs))
	for d := range dirs {
		keys = append(keys, d)
	}
	slices.Sort(keys)
	preferred := func(d string) bool { return strings.HasSuffix(filepath.ToSlash(d), "/Games/non-steam") }
	best, bestN := "", 0
	for _, d := range keys {
		n := countGameImages(d)
		switch {
		case n > bestN:
			best, bestN = d, n
		case n == bestN && n > 0 && preferred(d) && !preferred(best):
			best = d
		}
	}
	if bestN == 0 {
		return fallback
	}
	return best
}

// gamesDetected : dossier détecté, mis en cache gamesDetectTTL (champ protégé par gamesMu).
func (s *agentServer) gamesDetected(realHome string) string {
	s.gamesMu.Lock()
	defer s.gamesMu.Unlock()
	if s.gamesDetectedDir == "" || time.Since(s.gamesDetectedAt) > gamesDetectTTL {
		s.gamesDetectedDir = detectGamesDir(realHome, s.cfg.Roms)
		s.gamesDetectedAt = time.Now()
	}
	return s.gamesDetectedDir
}

// gamesDir : dossier d'installation effectif (absolu, sans liens, sous HOME). Le choix manuel
// (games_dir) l'emporte s'il est valide, sinon la détection. set : choix manuel en vigueur.
func (s *agentServer) gamesDir() (dir string, set bool, code int, msg string) {
	s.gamesMu.Lock()
	manual := s.cfg.GamesDir
	s.gamesMu.Unlock()
	if manual != "" {
		if real, c, _ := gamesDirCheck(manual); c == 0 {
			return real, true, 0, ""
		}
	}
	realHome, err := filetree.Home()
	if err != nil {
		return "", false, http.StatusInternalServerError, "HOME inaccessible"
	}
	dir, code, msg = gamesVerify(s.gamesDetected(realHome))
	return dir, false, code, msg
}

// gamesFree : octets disponibles sur le système de fichiers de dir (ou de son plus proche ancêtre).
func gamesFree(dir string) int64 {
	anc := filetree.Ancestor(dir)
	if anc == "" {
		return 0
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(anc, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// checkGamesName : nom d'image seul (sans dossier), en .wsquashfs, dont la tige est un nom de jeu valide.
// Retourne le nom, ou un message d'erreur (400).
func checkGamesName(name string) (string, string) {
	const want = "nom d'image invalide (Jeu.wsquashfs attendu)"
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "\\\x00") {
		return "", want
	}
	stem, ok := strings.CutSuffix(name, gamesExt)
	if !ok || !savesync.ValidGame(stem) {
		return "", want
	}
	return name, ""
}

// regularSize : taille d'un fichier ordinaire (sans suivre les liens) ; faux sinon.
func regularSize(p string) (int64, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	return fi.Size(), true
}

// takeUpload : réserve le nom pour un envoi (un seul envoi à la fois par nom).
func (s *agentServer) takeUpload(name string) bool {
	s.gamesMu.Lock()
	defer s.gamesMu.Unlock()
	if s.uploads == nil {
		s.uploads = map[string]bool{}
	}
	if s.uploads[name] {
		return false
	}
	s.uploads[name] = true
	return true
}

func (s *agentServer) releaseUpload(name string) {
	s.gamesMu.Lock()
	defer s.gamesMu.Unlock()
	delete(s.uploads, name)
}

// gamesList : GET /v1/games/list. Dossier absent : listes vides, espace du premier parent existant.
func (s *agentServer) gamesList(w http.ResponseWriter, r *http.Request) {
	dir, set, code, msg := s.gamesDir()
	if code != 0 {
		writeDetail(w, code, msg)
		return
	}
	type gameFile struct {
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		MTime int64  `json:"mtime"`
	}
	type partFile struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	games, parts := []gameFile{}, []partFile{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			switch {
			case strings.HasSuffix(e.Name(), gamesPartExt):
				parts = append(parts, partFile{Name: e.Name(), Size: info.Size()})
			case strings.HasSuffix(e.Name(), gamesExt):
				games = append(games, gameFile{Name: e.Name(), Size: info.Size(), MTime: info.ModTime().Unix()})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dir": dir, "set": set, "free": gamesFree(dir), "games": games, "parts": parts,
	})
}

type gamesDirRequest struct {
	Dir string `json:"dir"`
}

// gamesSetDir : POST /v1/games/dir {"dir": "<absolu>"} ou {"dir": ""} (retour à la détection).
func (s *agentServer) gamesSetDir(w http.ResponseWriter, r *http.Request) {
	var req gamesDirRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	manual := ""
	if req.Dir != "" {
		if _, code, msg := gamesDirCheck(req.Dir); code != 0 {
			writeDetail(w, code, msg)
			return
		}
		manual = filepath.Clean(req.Dir)
	}
	cfg := loadAgentConfig()
	cfg.GamesDir = manual
	if err := saveAgentConfig(cfg); err != nil {
		writeDetail(w, http.StatusInternalServerError, "configuration non enregistrée")
		return
	}
	s.gamesMu.Lock()
	s.cfg.GamesDir = manual
	s.gamesDetectedDir = "" // détection refaite au besoin
	s.gamesMu.Unlock()
	dir, set, code, msg := s.gamesDir()
	if code != 0 {
		writeDetail(w, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "set": set})
}

// gamesPart : GET /v1/games/part?name=. Taille du .part (0 sans envoi), image installée ou non et sa taille.
func (s *agentServer) gamesPart(w http.ResponseWriter, r *http.Request) {
	name, msg := checkGamesName(r.URL.Query().Get("name"))
	if msg != "" {
		writeDetail(w, http.StatusBadRequest, msg)
		return
	}
	dir, _, code, msg := s.gamesDir()
	if code != 0 {
		writeDetail(w, code, msg)
		return
	}
	partSize, _ := regularSize(filepath.Join(dir, name+".part"))
	size, installed := regularSize(filepath.Join(dir, name))
	if !installed {
		size = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{"part_size": partSize, "installed": installed, "size": size})
}

// gamesPut : PUT /v1/games/file?name=&offset= (en-tête X-Wsq-Total-Size) : ajoute le corps au .part.
// Le décalage doit être la taille déjà reçue (409 sinon, avec part_size pour reprendre).
func (s *agentServer) gamesPut(w http.ResponseWriter, r *http.Request) {
	name, msg := checkGamesName(r.URL.Query().Get("name"))
	if msg != "" {
		writeDetail(w, http.StatusBadRequest, msg)
		return
	}
	total, err := strconv.ParseInt(r.Header.Get("X-Wsq-Total-Size"), 10, 64)
	if err != nil || total <= 0 {
		writeDetail(w, http.StatusBadRequest, "taille totale attendue (en-tête X-Wsq-Total-Size)")
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 || offset > total {
		writeDetail(w, http.StatusBadRequest, "décalage invalide")
		return
	}
	dir, _, code, msg := s.gamesDir()
	if code != 0 {
		writeDetail(w, code, msg)
		return
	}
	if _, installed := regularSize(filepath.Join(dir, name)); installed {
		writeDetail(w, http.StatusConflict, "déjà installé")
		return
	}
	if !s.takeUpload(name) {
		writeDetail(w, http.StatusConflict, "envoi en cours")
		return
	}
	defer s.releaseUpload(name)
	// Dossier créé au premier envoi (sous HOME : le plus proche ancêtre existant est contrôlé).
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeDetail(w, http.StatusInternalServerError, "dossier d'installation impossible : "+err.Error())
		return
	}
	dir, code, msg = gamesVerify(dir)
	if code != 0 {
		writeDetail(w, code, msg)
		return
	}
	part := filepath.Join(dir, name+".part")
	cur, isFileOK := regularSize(part)
	if _, err := os.Lstat(part); err == nil && !isFileOK {
		writeDetail(w, http.StatusConflict, "fichier d'envoi invalide (pas un fichier ordinaire)")
		return
	}
	if offset != cur {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "décalage", "part_size": cur})
		return
	}
	remaining := total - offset
	if free := gamesFree(dir); remaining > free-gamesFreeMargin {
		writeJSON(w, http.StatusInsufficientStorage, map[string]any{"detail": "espace insuffisant", "free": free})
		return
	}
	// O_NOFOLLOW : un lien posé entre le contrôle et l'ouverture ne sera pas suivi.
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "fichier d'envoi impossible : "+err.Error())
		return
	}
	defer f.Close()
	// Un morceau peut être long : délai de lecture propre à cet envoi.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(gamesPutTimeout))
	r.Body = http.MaxBytesReader(w, r.Body, remaining)
	_, err = io.Copy(f, r.Body)
	// Ce qui a été reçu reste sur disque : c'est la reprise.
	serr := f.Sync()
	size := cur
	if fi, e := f.Stat(); e == nil {
		size = fi.Size()
	}
	if err == nil && serr != nil {
		err = serr
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"detail": "corps au-delà de la taille annoncée", "part_size": size})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "envoi interrompu", "part_size": size})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"part_size": size})
}

type gamesCommitRequest struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// syncDirPath : fsync du dossier (la création du fichier final est sur disque).
func syncDirPath(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// gamesCommit : POST /v1/games/commit {name, size, sha256}. Vérifie la taille et l'empreinte du .part,
// puis le range en image installée. Empreinte fausse : le .part est supprimé (422).
func (s *agentServer) gamesCommit(w http.ResponseWriter, r *http.Request) {
	var req gamesCommitRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	name, msg := checkGamesName(req.Name)
	if msg != "" {
		writeDetail(w, http.StatusBadRequest, msg)
		return
	}
	want := strings.ToLower(req.SHA256)
	if _, err := hex.DecodeString(want); err != nil || len(want) != 64 {
		writeDetail(w, http.StatusBadRequest, "empreinte sha256 attendue (64 caractères hexadécimaux)")
		return
	}
	dir, _, code, msg := s.gamesDir()
	if code != 0 {
		writeDetail(w, code, msg)
		return
	}
	if !s.takeUpload(name) {
		writeDetail(w, http.StatusConflict, "envoi en cours")
		return
	}
	defer s.releaseUpload(name)
	final := filepath.Join(dir, name)
	part := final + ".part"
	fi, err := os.Lstat(part)
	if errors.Is(err, fs.ErrNotExist) {
		writeDetail(w, http.StatusNotFound, "aucun envoi en cours pour "+name)
		return
	}
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "envoi illisible")
		return
	}
	if !fi.Mode().IsRegular() {
		writeDetail(w, http.StatusConflict, "fichier d'envoi invalide (pas un fichier ordinaire)")
		return
	}
	if fi.Size() != req.Size {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "taille", "part_size": fi.Size()})
		return
	}
	got, err := download.SHA256(part)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "lecture de l'envoi impossible")
		return
	}
	if got != want {
		_ = os.Remove(part)
		writeDetail(w, http.StatusUnprocessableEntity, "empreinte incorrecte, envoi à refaire")
		return
	}
	if _, err := os.Lstat(final); err == nil {
		writeDetail(w, http.StatusConflict, "déjà installé")
		return
	}
	if err := os.Rename(part, final); err != nil {
		writeDetail(w, http.StatusInternalServerError, "installation impossible : "+err.Error())
		return
	}
	syncDirPath(dir)
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "size": fi.Size()})
}

// gamesDelete : DELETE /v1/games/file?name= : supprime l'image installée et son .part. Jeu monté,
// lancé ou verrouillé : 409.
func (s *agentServer) gamesDelete(w http.ResponseWriter, r *http.Request) {
	name, msg := checkGamesName(r.URL.Query().Get("name"))
	if msg != "" {
		writeDetail(w, http.StatusBadRequest, msg)
		return
	}
	dir, _, code, msg := s.gamesDir()
	if code != 0 {
		writeDetail(w, code, msg)
		return
	}
	if !s.takeUpload(name) {
		writeDetail(w, http.StatusConflict, "envoi en cours")
		return
	}
	defer s.releaseUpload(name)
	lock, ok := lockIdleGame(strings.TrimSuffix(name, gamesExt))
	if !ok {
		writeDetail(w, http.StatusConflict, "jeu monté ou en cours")
		return
	}
	if lock != nil {
		defer lock.Close()
	}
	removed := false
	for _, p := range []string{filepath.Join(dir, name), filepath.Join(dir, name+".part")} {
		switch err := os.Remove(p); {
		case err == nil:
			removed = true
		case !errors.Is(err, fs.ErrNotExist):
			writeDetail(w, http.StatusInternalServerError, "suppression impossible : "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}
