package main

// Routes HTTPS de l'agent (voir agent.go). Chaque route appelle une action du
// lanceur ou une lecture de dossier borné ; aucune commande fournie par le
// client, aucun chemin hors des dossiers roms/ ou des runners installés.
// Les routes sont réparties par famille : pair_http.go, actions_http.go,
// runner_http.go, saves_http.go et games_http.go.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"wsquashfs-launcher/internal/savesync"
)

const doctorTTL = 5 * time.Minute

type agentServer struct {
	cfg       agentConfig
	logOn     bool
	trial     sync.Mutex // un essai à la fois
	docMu     sync.Mutex
	docOut    []byte
	docAt     time.Time
	pairMu    sync.Mutex // POST /v1/pair : un code consommé à la fois
	syncCfgMu sync.Mutex // sync.json : déclarations de dossiers et du manager (PUT/DELETE /v1/saves/dirs, PUT /v1/saves/manager)
	// Installation des images (games_http.go) : dossier détecté (cache de 10 min), envois en
	// cours par nom. gamesMu protège aussi cfg.GamesDir.
	gamesMu          sync.Mutex
	gamesDetectedDir string
	gamesDetectedAt  time.Time
	uploads          map[string]bool
}

func newAgentServer(cfg agentConfig, logOn bool) *agentServer {
	return &agentServer{cfg: cfg, logOn: logOn}
}

// httpServer : routes, authentification et trace.
func (s *agentServer) httpServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pair", s.pairRoute) // hors du contrôle de jeton : le code d'association
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
	mux.HandleFunc("GET /v1/saves/dirs", s.syncMachine(s.savesDirs))
	mux.HandleFunc("PUT /v1/saves/dirs", s.syncMachine(s.savesDirPut))
	mux.HandleFunc("DELETE /v1/saves/dirs", s.syncMachine(s.savesDirDelete))
	mux.HandleFunc("PUT /v1/saves/manager", s.syncMachine(s.savesManagerPut))
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

// auth : le jeton « Bearer » doit être celui d'une machine associée (agent.json, relu à chaque
// requête : un --pair forget ou reset prend effet tout de suite). Si l'en-tête X-Wsq-Machine est
// présent, il doit être l'identifiant de cette machine-là.
func (s *agentServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, ok := machineForToken(loadAgentConfig().Machines, r.Header.Get("Authorization"))
		if !ok {
			writeDetail(w, http.StatusUnauthorized, "jeton refusé")
			return
		}
		if id := r.Header.Get("X-Wsq-Machine"); id != "" && id != m.ID {
			writeDetail(w, http.StatusUnauthorized, "jeton refusé")
			return
		}
		next(w, r)
	}
}

// machineForToken : la machine dont le SHA-256 de jeton égale celui du jeton présenté (en-tête
// Authorization « Bearer »). La comparaison est à temps constant, toutes les machines sont lues.
func machineForToken(machines []agentMachine, authorization string) (agentMachine, bool) {
	token, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok || token == "" {
		return agentMachine{}, false
	}
	sum := sha256.Sum256([]byte(token))
	got := hex.EncodeToString(sum[:])
	var found agentMachine
	match := false
	for _, m := range machines {
		if subtle.ConstantTimeCompare([]byte(got), []byte(m.TokenSHA256)) == 1 && !match {
			found, match = m, true
		}
	}
	return found, match
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
	return conf.selfPath
}

func (s *agentServer) info(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	gamesDir, gamesSet, _, _ := s.gamesDir()
	// manager : empreinte du manager déclaré ("" sans déclaration) ; auto : synchro automatique avec le manager.
	cfg := savesync.LoadConfig()
	manager := ""
	if p, ok := savesync.ManagerPeer(cfg); ok {
		manager = p.Fingerprint
	}
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
		"manager":         manager,
		"auto":            cfg.Auto,
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

// doctor : --doctor json, mis en cache quelques minutes.
func (s *agentServer) doctor(w http.ResponseWriter, r *http.Request) {
	s.docMu.Lock()
	defer s.docMu.Unlock()
	if s.docOut == nil || r.URL.Query().Get("refresh") == "1" || time.Since(s.docAt) > doctorTTL {
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, launcherExe(), "--doctor", "json").Output()
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
