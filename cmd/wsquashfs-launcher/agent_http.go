package main

// Routes HTTPS de l'agent (voir agent.go). Chaque route appelle une action du
// lanceur ou une lecture de dossier borné ; aucune commande fournie par le
// client, aucun chemin hors des dossiers roms/ ou des runners installés.
// Les routes /v1/saves/* (synchro --sync, sync.go) lisent et écrivent les
// sauvegardes de partie, bornées au périmètre de sync.go : jamais hors de
// users/, jamais les caches ni les comptes système.

import (
	"archive/tar"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const doctorTTL = 5 * time.Minute

// trialStopDelay : temps laissé à --trial interrompu pour arrêter le jeu
// (5 s pour les processus, 30 s pour le démontage, marge).
const trialStopDelay = 60 * time.Second

type agentServer struct {
	cfg    agentConfig
	logOn  bool
	last   atomic.Int64 // dernière requête (UnixNano)
	busy   atomic.Int32 // requêtes en cours (pas d'arrêt pendant un essai)
	trial  sync.Mutex   // un essai à la fois
	docMu  sync.Mutex
	docOut []byte
	docAt  time.Time
	// last-client.json : écrit à chaque requête authentifiée (noteClient).
	clientMu      sync.Mutex
	clientAddr    string
	clientMachine string
	clientAt      time.Time
	// Installation des images (games_http.go) : dossier détecté (cache de 10 min), envois en
	// cours par nom. gamesMu protège aussi cfg.GamesDir.
	gamesMu          sync.Mutex
	gamesDetectedDir string
	gamesDetectedAt  time.Time
	uploads          map[string]bool
}

func newAgentServer(cfg agentConfig, logOn bool) *agentServer {
	s := &agentServer{cfg: cfg, logOn: logOn}
	s.last.Store(time.Now().UnixNano())
	return s
}

// httpServer : routes, authentification et trace.
func (s *agentServer) httpServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", s.auth(s.info))
	mux.HandleFunc("GET /v1/doctor", s.auth(s.doctor))
	mux.HandleFunc("POST /v1/trial", s.auth(s.trialRoute))
	mux.HandleFunc("POST /v1/maintenance", s.auth(s.maintenance))
	mux.HandleFunc("GET /v1/runner/list", s.auth(s.runnerList))
	mux.HandleFunc("GET /v1/runner/tar", s.auth(s.runnerTar))
	mux.HandleFunc("GET /v1/saves/list", s.syncMachine(s.savesList))
	mux.HandleFunc("GET /v1/saves/tar", s.syncMachine(s.savesGet))
	mux.HandleFunc("PUT /v1/saves/tar", s.syncMachine(s.savesPut))
	mux.HandleFunc("DELETE /v1/saves/tar", s.syncMachine(s.savesDelete))
	mux.HandleFunc("POST /v1/saves/state", s.syncMachine(s.savesState))
	mux.HandleFunc("GET /v1/games/list", s.auth(s.gamesList))
	mux.HandleFunc("POST /v1/games/dir", s.auth(s.gamesSetDir))
	mux.HandleFunc("GET /v1/games/part", s.auth(s.gamesPart))
	mux.HandleFunc("PUT /v1/games/file", s.auth(s.gamesPut))
	mux.HandleFunc("POST /v1/games/commit", s.auth(s.gamesCommit))
	mux.HandleFunc("DELETE /v1/games/file", s.auth(s.gamesDelete))
	var h http.Handler = mux
	if s.logOn {
		h = s.logged(mux)
	}
	// ReadTimeout borne la lecture de la requête seulement : net/http lève le
	// délai en lançant sa lecture de fond, un essai long garde son contexte
	// (TestReadTimeoutKeepsLongRequestAlive).
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 20 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
}

// auth : jeton « Bearer » comparé à temps constant ; la configuration est
// relue à chaque requête (un --pair --reset prend effet tout de suite).
func (s *agentServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.last.Store(time.Now().UnixNano())
		s.busy.Add(1)
		defer s.busy.Add(-1)
		want := "Bearer " + loadAgentConfig().Token
		got := r.Header.Get("Authorization")
		if loadAgentConfig().Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeDetail(w, http.StatusUnauthorized, "jeton refusé")
			return
		}
		s.noteClient(r)
		next(w, r)
	}
}

// noteClient : note la requête authentifiée dans last-client.json (--pair attend
// cette trace). Au plus une fois par seconde pour un même client ; un autre
// client (adresse ou machine) est noté tout de suite.
func (s *agentServer) noteClient(r *http.Request) {
	addr, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		addr = r.RemoteAddr
	}
	machine := r.Header.Get("X-Wsq-Machine")
	now := time.Now()
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if addr == s.clientAddr && machine == s.clientMachine && now.Sub(s.clientAt) < time.Second {
		return
	}
	s.clientAddr, s.clientMachine, s.clientAt = addr, machine, now
	_ = writeLastClient(lastClient{At: now, Addr: addr, Machine: machine})
}

// logged : trace (méthode, chemin, code, durée) ; jamais le jeton ni la requête.
func (s *agentServer) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		fmt.Fprintf(os.Stderr, "%s %s %d %dms\n", r.Method, r.URL.Path, rec.code, time.Since(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// idleWatchdog : arrêt après agentIdleExit sans requête (socket systemd).
func (s *agentServer) idleWatchdog(srv *http.Server) {
	for range time.Tick(5 * time.Second) {
		idle := time.Since(time.Unix(0, s.last.Load()))
		if s.busy.Load() == 0 && idle > agentIdleExit {
			_ = srv.Shutdown(context.Background())
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeDetail(w http.ResponseWriter, code int, detail string) {
	writeJSON(w, code, map[string]string{"detail": detail})
}

// launcherExe : ce binaire, pour lancer ses propres actions.
func launcherExe() string {
	return selfPath
}

func (s *agentServer) info(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	gamesDir, gamesSet, _, _ := s.gamesDir()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":         agentVersion,
		"host":            host,
		"user":            agentOwner(),
		"launcher":        launcherExe(),
		"roms":            s.cfg.Roms,
		"session_process": s.cfg.Session,
		"os":              osID("/etc/os-release"),
		"games_dir":       gamesDir,
		"games_dir_set":   gamesSet,
	})
}

// osID : valeur de ID= dans un os-release (guillemets retirés, minuscules) ; "linux" si le fichier
// est absent ou sans ID.
func osID(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "linux"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ID="); ok {
			if id := strings.ToLower(strings.Trim(strings.TrimSpace(v), `"'`)); id != "" {
				return id
			}
		}
	}
	return "linux"
}

// doctor : --doctor --json, mis en cache quelques minutes.
func (s *agentServer) doctor(w http.ResponseWriter, r *http.Request) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	if s.docOut == nil || r.URL.Query().Get("refresh") == "1" || time.Since(s.docAt) > doctorTTL {
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, launcherExe(), "--doctor", "--json").Output()
		if len(out) == 0 {
			writeDetail(w, http.StatusServiceUnavailable, "rapport --doctor indisponible")
			return
		}
		s.docOut, s.docAt = out, time.Now()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(s.docOut)
}

// resolveImage : chemin relatif à un dossier roms/ configuré → fichier réel.
// Refuse les chemins absolus, « .. », les liens qui sortent des dossiers et
// tout ce qui n'est pas une image .wsquashfs.
func (s *agentServer) resolveImage(rel string) (string, int, string) {
	if rel == "" || strings.HasPrefix(rel, "/") {
		return "", http.StatusBadRequest, "chemin invalide"
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", http.StatusBadRequest, "chemin invalide"
		}
	}
	if !strings.HasSuffix(rel, ".wsquashfs") {
		return "", http.StatusBadRequest, "pas une image .wsquashfs"
	}
	for _, root := range s.cfg.Roms {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		real, err := filepath.EvalSymlinks(filepath.Join(realRoot, rel))
		if err != nil {
			continue
		}
		if within(real, realRoot) && isFile(real) {
			return real, 0, ""
		}
	}
	return "", http.StatusNotFound, "image introuvable : " + rel
}

// within : vrai si p est base ou un chemin dessous.
func within(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

type trialRequest struct {
	Rel      string `json:"rel"`
	Duration int    `json:"duration"`
}

// trialRoute : essai chronométré d'une image des dossiers roms/.
func (s *agentServer) trialRoute(w http.ResponseWriter, r *http.Request) {
	var req trialRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req); err != nil {
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	if req.Duration == 0 {
		req.Duration = 60
	}
	if req.Duration < 10 || req.Duration > 600 {
		writeDetail(w, http.StatusBadRequest, "durée hors limites (10 à 600 s)")
		return
	}
	image, code, msg := s.resolveImage(req.Rel)
	if code != 0 {
		writeDetail(w, code, msg)
		return
	}
	if !s.trial.TryLock() {
		writeDetail(w, http.StatusConflict, "un essai est déjà en cours sur cette machine")
		return
	}
	defer s.trial.Unlock()

	shot, err := os.CreateTemp("", "wsfs-shot.*.png")
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "erreur interne de l'agent")
		return
	}
	shot.Close()
	defer os.Remove(shot.Name())

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.Duration+180)*time.Second)
	defer cancel()
	args := []string{"--trial", image, fmt.Sprint(req.Duration), "--session", s.cfg.Session, "--screenshot", shot.Name()}
	cmd := exec.CommandContext(ctx, launcherExe(), args...)
	// Interruption (délai, client parti) : SIGTERM, pour que --trial arrête
	// le jeu et démonte ; SIGKILL seulement s'il ne s'est pas arrêté à temps.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = trialStopDelay
	out, err := cmd.Output()
	if ctx.Err() != nil {
		writeDetail(w, http.StatusGatewayTimeout, fmt.Sprintf("essai trop long (%d s)", req.Duration))
		return
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			switch ee.ExitCode() {
			case 3:
				writeDetail(w, http.StatusConflict, "un jeu tourne déjà sur cette machine")
				return
			case 4:
				writeDetail(w, http.StatusNotFound, "image introuvable")
				return
			}
		}
	}
	var report json.RawMessage
	if json.Unmarshal(out, &report) != nil {
		writeDetail(w, http.StatusInternalServerError, "rapport d'essai illisible")
		return
	}
	var screenshot any
	if png, err := os.ReadFile(shot.Name()); err == nil && len(png) > 8 {
		screenshot = base64.StdEncoding.EncodeToString(png)
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": report, "screenshot": screenshot})
}

// gameRule : la place du jeu (rel) dans une action de maintenance.
type gameRule int

const (
	noGame       gameRule = iota // rel interdit
	gameRequired                 // rel obligatoire (son image devient l'argument)
	gameOptional                 // rel facultatif
)

// maintenanceSpec : arguments du lanceur pour une action de l'agent (sans le jeu) et règle sur le jeu.
type maintenanceSpec struct {
	args []string
	game gameRule
}

// maintenanceActions : liste fermée, action de l'agent → lanceur (pas de --clear, --erase, --pack,
// --install). Le contrat est celui de wsquashfs-manager ; la version de l'agent (agentVersion) le suit.
var maintenanceActions = map[string]maintenanceSpec{
	"list":        {[]string{"--list"}, noGame},
	"umount":      {[]string{"--umount"}, noGame},
	"clean":       {[]string{"--clean"}, noGame},
	"save":        {[]string{"--save"}, gameRequired},
	"saves":       {[]string{"--saves"}, noGame},
	"sync":        {[]string{"--sync"}, gameOptional},
	"sync-status": {[]string{"--sync", "status"}, noGame},
}

type maintenanceRequest struct {
	Action string `json:"action"`
	Rel    string `json:"rel"`
}

// maintenance : une action de maintenance du lanceur, selon la table maintenanceActions.
func (s *agentServer) maintenance(w http.ResponseWriter, r *http.Request) {
	var req maintenanceRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req); err != nil {
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	spec, ok := maintenanceActions[req.Action]
	if !ok {
		writeDetail(w, http.StatusBadRequest, "action de maintenance non autorisée")
		return
	}
	args := append([]string{}, spec.args...)
	switch {
	case spec.game == noGame && req.Rel != "":
		writeDetail(w, http.StatusBadRequest, req.Action+" ne prend pas de jeu")
		return
	case spec.game == gameRequired && req.Rel == "":
		writeDetail(w, http.StatusBadRequest, "--save attend un jeu")
		return
	case req.Rel != "":
		image, code, msg := s.resolveImage(req.Rel)
		if code != 0 {
			writeDetail(w, code, msg)
			return
		}
		args = append(args, image)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 900*time.Second)
	defer cancel()
	// Couleurs forcées : wsquashfs-manager affiche la sortie en couleurs
	// (codes ANSI convertis en HTML). NO_COLOR, s'il est posé, l'emporte.
	cmd := exec.CommandContext(ctx, launcherExe(), args...)
	cmd.Env = append(os.Environ(), "CLICOLOR_FORCE=1")
	out, err := cmd.CombinedOutput()
	// code : code de sortie du lanceur, comme en mode local (wsquashfs-manager teste code !== 0).
	// -1 si le processus n'a pas pu démarrer ou a été tué (délai) : le motif est ajouté à la sortie.
	code := 0
	if err != nil {
		code = -1
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() >= 0 {
			code = ee.ExitCode()
		} else {
			out = append(out, []byte(err.Error()+"\n")...)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": req.Action, "code": code, "output": string(out)})
}

// runnerDir : dossier demandé, seulement s'il est un runner installé.
func runnerDir(path string) (string, int, string) {
	if !strings.HasPrefix(path, "/") {
		return "", http.StatusBadRequest, "chemin absolu requis"
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", http.StatusNotFound, "dossier introuvable"
	}
	for _, base := range listWineRunners() {
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

func (s *agentServer) runnerList(w http.ResponseWriter, r *http.Request) {
	real, code, msg := runnerDir(r.URL.Query().Get("path"))
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
func (s *agentServer) runnerTar(w http.ResponseWriter, r *http.Request) {
	real, code, msg := runnerDir(r.URL.Query().Get("path"))
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

// syncMaxBody : taille maximale d'une archive reçue (--sync). Variable pour les tests.
var syncMaxBody int64 = 2 << 30

// syncStateRoot : état des synchros, par machine cliente (dossier caché de savesDir).
func syncStateRoot() string {
	return filepath.Join(savesDir, ".sync")
}

// syncMachine : authentification, puis identifiant de la machine cliente obligatoire
// (en-tête X-Wsq-Machine) : l'état de synchro est tenu par machine.
func (s *agentServer) syncMachine(next http.HandlerFunc) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		if !validSyncID(r.Header.Get("X-Wsq-Machine")) {
			writeDetail(w, http.StatusBadRequest, "identifiant de machine absent ou invalide")
			return
		}
		next(w, r)
	})
}

// remoteSave : un jeu tel que la machine le voit pour la synchro (routes /v1/saves/list).
type remoteSave struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Newest      int64  `json:"newest"`
	Size        int64  `json:"size"`
	State       string `json:"state"`
}

// savesList : jeux à sauvegardes, plus ceux qui ont un état pour cette machine.
func (s *agentServer) savesList(w http.ResponseWriter, r *http.Request) {
	machine := r.Header.Get("X-Wsq-Machine")
	// Jeux avec un état pour cette machine : listés même vides (la synchro doit le savoir).
	withState := map[string]bool{}
	if entries, err := os.ReadDir(filepath.Join(syncStateRoot(), machine)); err == nil {
		for _, e := range entries {
			if g, ok := strings.CutSuffix(e.Name(), ".json"); ok {
				withState[g] = true
			}
		}
	}
	// Même liste que --sync (syncGames), plus les jeux qui n'ont qu'un état pour cette machine.
	names := map[string]bool{}
	for _, g := range syncGames() {
		names[g] = true
	}
	for g := range withState {
		names[g] = true
	}
	games := []remoteSave{}
	for name := range names {
		if !validSyncGame(name) {
			continue
		}
		users := syncUsersDir(name)
		files := collectSaveFiles(users)
		fp := saveFingerprint(users, files)
		if fp == "" && !withState[name] {
			continue
		}
		st := loadSyncState(syncStateRoot(), machine, name)
		games = append(games, remoteSave{Name: name, Fingerprint: fp, Newest: newestSave(files), Size: saveSize(files), State: st.Fingerprint})
	}
	sort.Slice(games, func(i, j int) bool { return games[i].Name < games[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"games": games})
}

// savesGet : archive des sauvegardes d'un jeu (tar.gz). L'archive est écrite dans un fichier
// temporaire d'abord : l'empreinte et le profil partent dans les en-têtes, avant le corps.
func (s *agentServer) savesGet(w http.ResponseWriter, r *http.Request) {
	game := r.URL.Query().Get("game")
	if !validSyncGame(game) {
		writeDetail(w, http.StatusBadRequest, "jeu invalide")
		return
	}
	lock, ok := lockIdleGame(game)
	if !ok {
		writeDetail(w, http.StatusConflict, "jeu en cours")
		return
	}
	if lock != nil {
		defer lock.Close()
	}
	users := syncUsersDir(game)
	files := collectSaveFiles(users)
	if len(files) == 0 {
		writeDetail(w, http.StatusNotFound, "aucune sauvegarde pour ce jeu")
		return
	}
	fp := saveFingerprint(users, files)
	tmp, err := os.CreateTemp("", "wsq-sync-*.tar.gz")
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "erreur interne de l'agent")
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	profile, err := writeSaveArchive(tmp, users, files)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "archive impossible : "+game)
		return
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "erreur interne de l'agent")
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeDetail(w, http.StatusInternalServerError, "erreur interne de l'agent")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", fmt.Sprint(size))
	w.Header().Set("X-Wsq-Fingerprint", fp)
	if profile != "" {
		w.Header().Set("X-Wsq-Profile", profile)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, tmp)
}

// savesPut : reçoit une archive et remplace le périmètre du jeu. Si le périmètre local a changé
// depuis la dernière synchro avec cette machine, il est archivé d'abord (dossier backups) : c'est
// la version perdante du conflit, rien ne doit disparaître. Sans archive, rien n'est remplacé.
func (s *agentServer) savesPut(w http.ResponseWriter, r *http.Request) {
	game := r.URL.Query().Get("game")
	if !validSyncGame(game) {
		writeDetail(w, http.StatusBadRequest, "jeu invalide")
		return
	}
	machine := r.Header.Get("X-Wsq-Machine")
	lock, ok := lockIdleGame(game)
	if !ok {
		writeDetail(w, http.StatusConflict, "jeu en cours")
		return
	}
	if lock != nil {
		defer lock.Close()
	}
	users := syncUsersDir(game)
	cur := saveFingerprint(users, collectSaveFiles(users))
	archived := ""
	if cur != "" && cur != loadSyncState(syncStateRoot(), machine, game).Fingerprint {
		// Le membre est le nom réel du dossier : une mise de côté (mode copy) ne s'appelle pas « users ».
		path, err := archiveUsers(game, filepath.Dir(users), filepath.Base(users))
		if err != nil {
			writeDetail(w, http.StatusInternalServerError, "archivage de la version locale impossible, rien remplacé")
			return
		}
		archived = path
	}
	r.Body = http.MaxBytesReader(w, r.Body, syncMaxBody)
	if err := applySaveArchive(r.Body, users, r.Header.Get("X-Wsq-Profile")); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeDetail(w, http.StatusRequestEntityTooLarge, "archive trop volumineuse")
			return
		}
		writeDetail(w, http.StatusBadRequest, "archive refusée : "+err.Error())
		return
	}
	fp := saveFingerprint(users, collectSaveFiles(users))
	if err := saveSyncState(syncStateRoot(), machine, game, fp); err != nil {
		writeDetail(w, http.StatusInternalServerError, "état de synchro non enregistré")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fingerprint": fp, "archived": archived})
}

// savesDelete : supprime les sauvegardes d'un jeu sur cette machine (le manager, par l'agent). Le
// périmètre est archivé d'abord (dossier backups) : si l'archivage échoue, rien n'est effacé. Seuls
// les fichiers du périmètre partent, puis les dossiers devenus vides ; caches et autres fichiers
// restent. L'état de synchro de la machine appelante pour ce jeu est effacé ; ceux des autres machines
// restent.
func (s *agentServer) savesDelete(w http.ResponseWriter, r *http.Request) {
	game := r.URL.Query().Get("game")
	if !validSyncGame(game) {
		writeDetail(w, http.StatusBadRequest, "jeu invalide")
		return
	}
	machine := r.Header.Get("X-Wsq-Machine")
	lock, ok := lockIdleGame(game)
	if !ok {
		writeDetail(w, http.StatusConflict, "jeu en cours")
		return
	}
	if lock != nil {
		defer lock.Close()
	}
	users := syncUsersDir(game)
	files := collectSaveFiles(users)
	if len(files) == 0 {
		if err := removeSyncState(syncStateRoot(), machine, game); err != nil {
			writeDetail(w, http.StatusInternalServerError, "état de synchro non supprimé")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"archived": "", "removed": 0})
		return
	}
	archived, err := archiveUsers(game, filepath.Dir(users), filepath.Base(users))
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "archivage impossible, rien supprimé")
		return
	}
	removed := 0
	for _, f := range files {
		profile, _ := splitRel(f.Rel)
		p := filepath.Join(users, filepath.FromSlash(f.Rel))
		switch err := os.Remove(p); {
		case err == nil:
			removed++
		case !errors.Is(err, fs.ErrNotExist):
			writeDetail(w, http.StatusInternalServerError, "suppression impossible ("+err.Error()+"), archive : "+archived)
			return
		}
		removeEmptyDirs(filepath.Dir(p), filepath.Join(users, profile))
	}
	if err := removeSyncState(syncStateRoot(), machine, game); err != nil {
		writeDetail(w, http.StatusInternalServerError, "état de synchro non supprimé, archive : "+archived)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"archived": archived, "removed": removed})
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

// syncStateRequest : corps de POST /v1/saves/state.
type syncStateRequest struct {
	Game        string `json:"game"`
	Fingerprint string `json:"fingerprint"`
}

// savesState : la machine cliente confirme l'empreinte commune après une réception. Refusé si le
// jeu a changé sur cette machine entre-temps.
func (s *agentServer) savesState(w http.ResponseWriter, r *http.Request) {
	var req syncStateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	if !validSyncGame(req.Game) {
		writeDetail(w, http.StatusBadRequest, "jeu invalide")
		return
	}
	users := syncUsersDir(req.Game)
	cur := saveFingerprint(users, collectSaveFiles(users))
	if cur != req.Fingerprint {
		writeDetail(w, http.StatusConflict, "le jeu a changé sur cette machine depuis la lecture : relancer --sync")
		return
	}
	if err := saveSyncState(syncStateRoot(), r.Header.Get("X-Wsq-Machine"), req.Game, cur); err != nil {
		writeDetail(w, http.StatusInternalServerError, "état de synchro non enregistré")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fingerprint": cur})
}
