package main

// Routes HTTPS de l'agent (voir agent.go). Chaque route appelle une action du
// lanceur ou une lecture de dossier borné ; aucune commande fournie par le
// client, aucun chemin hors des dossiers roms/ ou des runners installés.

import (
	"archive/tar"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
		next(w, r)
	}
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
	writeJSON(w, http.StatusOK, map[string]any{
		"version":         agentVersion,
		"host":            host,
		"user":            agentOwner(),
		"launcher":        launcherExe(),
		"roms":            s.cfg.Roms,
		"session_process": s.cfg.Session,
	})
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

// maintenanceActions : liste fermée (pas de --clear, --pack, --install).
var maintenanceActions = map[string]bool{"list": true, "umount": true, "clean": true, "saves": true}

type maintenanceRequest struct {
	Action string `json:"action"`
	Rel    string `json:"rel"`
}

// maintenance : une action de maintenance du lanceur ; saves accepte un jeu.
func (s *agentServer) maintenance(w http.ResponseWriter, r *http.Request) {
	var req maintenanceRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req); err != nil {
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	if !maintenanceActions[req.Action] {
		writeDetail(w, http.StatusBadRequest, "action de maintenance non autorisée")
		return
	}
	args := []string{"--" + req.Action}
	if req.Rel != "" {
		if req.Action != "saves" {
			writeDetail(w, http.StatusBadRequest, "--"+req.Action+" ne prend pas de jeu")
			return
		}
		image, code, msg := s.resolveImage(req.Rel)
		if code != 0 {
			writeDetail(w, code, msg)
			return
		}
		args = append(args, image)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 900*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, launcherExe(), args...).CombinedOutput()
	writeJSON(w, http.StatusOK, map[string]any{"action": req.Action, "output": string(out)})
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
	defer tw.Close()
	top := filepath.Base(real)
	_ = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
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
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return nil
			}
			defer f.Close()
			_, _ = io.Copy(tw, f)
		}
		return nil
	})
}
