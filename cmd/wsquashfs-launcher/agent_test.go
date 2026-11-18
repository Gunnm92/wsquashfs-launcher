package main

// Tests de l'agent (internal/agent) qui ont besoin du lanceur : synchro des sauvegardes, état du
// lanceur. Les routes qui ne touchent que l'agent sont testées dans internal/agent.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wsquashfs-launcher/internal/agent"
	"wsquashfs-launcher/internal/savesync"
)

// agentFixture : configuration isolée, une racine roms/ avec une image, et un faux lanceur (pour
// les routes qui l'appellent). Une machine associée : client-machine-01, jeton « secret-token ».
func agentFixture(t *testing.T) (*agent.Server, http.Handler, string) {
	t.Helper()
	isolateState(t)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("HOME", dir)
	t.Setenv("WSQUASHFS_SYSROOT", filepath.Join(dir, "sysroot"))
	roms := filepath.Join(dir, "roms")
	if err := os.MkdirAll(filepath.Join(roms, "win"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roms, "win", "Jeu.wsquashfs"), []byte("hsqs"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "fake-launcher")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho '{\"code\":0,\"sections\":[],\"runners\":[],\"verdict\":\"ok\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	conf.selfPath = fake
	s := agentSrv()
	cfg := agent.Config{Port: 8790, Roms: []string{roms}, Session: "emulationstation"}
	sum := sha256.Sum256([]byte("secret-token"))
	cfg.Machines = []agent.Machine{{ID: "client-machine-01", Name: "poste-test", TokenSHA256: hex.EncodeToString(sum[:]), Paired: time.Now()}}
	if err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Init(nil, 0); err != nil {
		t.Fatal(err)
	}
	return s, s.HTTPServer(cfg, false).Handler, roms
}

// enrollMachine : enregistre une machine associée dans agent.json, comme le fait la route POST
// /v1/pair (jeton haché, identifiant, nom).
func enrollMachine(t *testing.T, id, name, token string) {
	t.Helper()
	s := agentSrv()
	cfg := s.LoadConfig()
	sum := sha256.Sum256([]byte(token))
	cfg.Machines = append(cfg.Machines, agent.Machine{ID: id, Name: name, TokenSHA256: hex.EncodeToString(sum[:]), Paired: time.Now()})
	if err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

// beginPairing : écrit le code d'association en attente (pair.json), valable ttl, comme --pair.
func beginPairing(secret string, ttl time.Duration) error {
	sum := sha256.Sum256([]byte(secret))
	data, err := json.Marshal(map[string]any{"secret_sha256": hex.EncodeToString(sum[:]), "expires": time.Now().Add(ttl)})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(agentSrv().Dir(), "pair.json"), data, 0o600)
}

// writeJSON et writeDetail : réponses des faux agents des tests (même forme que l'agent).
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeDetail(w http.ResponseWriter, code int, detail string) {
	writeJSON(w, code, map[string]string{"detail": detail})
}

// detailOf : le « detail » d'une réponse d'erreur.
func detailOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var v struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("détail illisible : %s", rec.Body.String())
	}
	return v.Detail
}

// freePort : un port TCP libre.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func agentListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func pathExistsFile(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// agentPidFile : fichier de PID de l'agent.
func agentPidFile() string {
	return filepath.Join(agentSrv().Dir(), "agent.pid")
}

// startServedAgent : serveAgent dans ce processus, sur un port libre (configuration, sauvegardes
// et cache isolés). Rend un client de synchro qui le vise, le users/ du jeu « Game », le crochet
// d'interruption posé par l'agent (ce que SIGTERM appelle, voir installSignals) et son code de fin.
func startServedAgent(t *testing.T) (*savesync.Client, string, func(), <-chan int) {
	t.Helper()
	agentFixture(t)
	capteUI(t)
	isolateState(t)
	conf.savesDir, conf.cacheDir = t.TempDir(), t.TempDir()
	t.Setenv("WSQUASHFS_BACKUP_DIR", t.TempDir())
	cfg := agentSrv().LoadConfig()
	cfg.Port = freePort(t)
	if err := agentSrv().SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- agentSrv().Run([]string{"run"}) }()
	for deadline := time.Now().Add(5 * time.Second); !pathExistsFile(agentPidFile()) || !agentListening(cfg.Port); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("agent pas en écoute")
		}
	}
	hook := interruptHook.Load()
	if hook == nil {
		t.Fatal("agent en écoute sans crochet d'interruption : un signal le ferait sortir sans arrêt propre")
	}
	fp, err := agentSrv().CertFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	peer := savesync.Peer{Name: "agent", URL: fmt.Sprintf("https://127.0.0.1:%d", cfg.Port), Token: "secret-token", Fingerprint: fp}
	users := filepath.Join(conf.savesDir, syncRigGame, "drive_c", "users")
	syncTestPopulate(t, users, "steamuser")
	return syncer().NewClient(peer, "client-machine-01"), users, *hook, done
}

// TestServeAgentStopsGracefully : un signal d'arrêt pendant un envoi de sauvegardes ; le port se
// ferme, la requête en cours va au bout (200), puis l'agent rend 0 et retire son fichier de PID.
func TestServeAgentStopsGracefully(t *testing.T) {
	client, users, stop, done := startServedAgent(t)
	port := agentSrv().LoadConfig().Port
	archive := syncTestSaveArchive(t)
	closed := false
	body := &slowBody{data: archive, pause: func() {
		time.Sleep(200 * time.Millisecond) // l'agent a reçu le début du corps
		stop()
		time.Sleep(500 * time.Millisecond) // l'arrêt attend la requête
		closed = !agentListening(port)
	}}
	resp, err := client.Do(http.MethodPut, "/v1/saves/tar", url.Values{"game": {syncRigGame}}, body,
		map[string]string{"X-Wsq-Profile": "deck"})
	if err != nil {
		t.Fatalf("requête en cours coupée par l'arrêt : %v", err)
	}
	resp.Body.Close()
	if !closed {
		t.Error("port encore ouvert pendant l'arrêt")
	}
	select {
	case rc := <-done:
		if rc != 0 {
			t.Errorf("code %d, attendu 0", rc)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent toujours en cours après l'arrêt")
	}
	if pathExistsFile(agentPidFile()) {
		t.Error("fichier de PID gardé après l'arrêt")
	}
	if interruptHook.Load() != nil {
		t.Error("crochet d'interruption non retiré")
	}
	got, err := os.ReadFile(filepath.Join(users, "steamuser", "Documents", "Game", "save1.sav"))
	if err != nil || string(got) != "sauvegarde" {
		t.Errorf("sauvegarde reçue : %q, %v", got, err)
	}
}

// TestServeAgentStopCutsAfterGrace : un envoi qui ne finit pas dans le délai de grâce est coupé,
// l'agent s'arrête quand même (dans l'attente de --agent stop) et rien n'a été remplacé.
func TestServeAgentStopCutsAfterGrace(t *testing.T) {
	old := agentStopGrace
	agentStopGrace = 300 * time.Millisecond
	t.Cleanup(func() { agentStopGrace = old })
	client, users, stop, done := startServedAgent(t)
	before := saveFingerprint(users, savesync.CollectSaveFiles(users))
	archive := syncTestSaveArchive(t)
	release := make(chan struct{})
	body := &slowBody{data: archive, pause: func() {
		time.Sleep(200 * time.Millisecond)
		stop()
		<-release // le corps ne finit jamais avant l'arrêt
	}}
	errc := make(chan error, 1)
	go func() {
		resp, err := client.Do(http.MethodPut, "/v1/saves/tar", url.Values{"game": {syncRigGame}}, body,
			map[string]string{"X-Wsq-Profile": "deck"})
		if err == nil {
			resp.Body.Close()
		}
		errc <- err
	}()
	select {
	case rc := <-done:
		if rc != 0 {
			t.Errorf("code %d, attendu 0", rc)
		}
	case <-time.After(5 * time.Second): // attente de « --agent stop »
		t.Fatal("agent toujours en cours : --agent stop le dirait en échec")
	}
	close(release)
	if err := <-errc; err == nil {
		t.Error("envoi coupé accepté")
	}
	if pathExistsFile(agentPidFile()) {
		t.Error("fichier de PID gardé après l'arrêt")
	}
	if got := saveFingerprint(users, savesync.CollectSaveFiles(users)); got != before {
		t.Error("sauvegardes modifiées par un envoi coupé")
	}
}
