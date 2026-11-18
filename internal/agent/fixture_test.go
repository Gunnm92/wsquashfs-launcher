package agent

// Fixtures des tests de l'agent : environnement isolé (configuration, faux lanceur) sans le lanceur.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wsquashfs-launcher/internal/savesync"
	"wsquashfs-launcher/internal/ui"
)

// testExe : le faux lanceur du test en cours (Env.Exe).
var testExe string

// testLockedGame : le jeu dont le verrou est pris (Env.LockIdleGame).
var testLockedGame string

// interruptHook : le crochet posé par l'agent (Env.SetInterrupt).
var interruptHook atomic.Pointer[func()]

// testServer : l'agent avec un environnement de test : faux lanceur, aucune synchro, aucun runner.
func testServer() *Server {
	return New(Env{
		Exe:    func() string { return testExe },
		Syncer: func() *savesync.Syncer { return savesync.New(savesync.Env{}) },
		ListRunners: func() []string {
			dirs, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/wine/*"))
			return dirs
		},
		LockIdleGame: func(name string) (*os.File, bool) { return nil, name != testLockedGame },
		SetInterrupt: func(hook func()) {
			if hook == nil {
				interruptHook.Store(nil)
				return
			}
			interruptHook.Store(&hook)
		},
	})
}

// isolateState : testExe est rendu tel quel au nettoyage.
func isolateState(t *testing.T) {
	t.Helper()
	old := testExe
	t.Cleanup(func() { testExe, testLockedGame = old, "" })
}

// capteUI remplace les sorties ui par des tampons, rendus au nettoyage.
func capteUI(t *testing.T) (out, errs *bytes.Buffer) {
	t.Helper()
	out, errs = &bytes.Buffer{}, &bytes.Buffer{}
	t.Cleanup(ui.SetOutput(out, errs))
	return out, errs
}

// agentFixture : configuration isolée, une racine roms/ avec une image, et un
// faux lanceur (pour les routes qui l'appellent).
func agentFixture(t *testing.T) (*service, http.Handler, string) {
	t.Helper()
	isolateState(t)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("HOME", dir)
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
	testExe = fake
	cfg := Config{Port: 8790, Roms: []string{roms}, Session: "emulationstation"}
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := ensureAgentCert(); err != nil {
		t.Fatal(err)
	}
	// Une machine associée (client-machine-01, jeton « secret-token ») : agent.json ne garde que son SHA-256.
	enrollMachine(t, "client-machine-01", "poste-test", "secret-token")
	s := testServer().newService(cfg, false)
	return s, s.httpServer().Handler, roms
}

// enrollMachine : enregistre une machine associée dans agent.json, comme le fait la route POST
// /v1/pair (jeton haché, identifiant, nom).
func enrollMachine(t *testing.T, id, name, token string) {
	t.Helper()
	cfg := loadConfig()
	sum := sha256.Sum256([]byte(token))
	cfg.Machines = upsertMachine(cfg.Machines, Machine{
		ID: id, Name: name, TokenSHA256: hex.EncodeToString(sum[:]), Paired: time.Now(),
	})
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

func do(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	return doAs(h, method, path, token, "", body)
}

// doAs : requête au handler avec un jeton et un en-tête X-Wsq-Machine facultatifs.
func doAs(h http.Handler, method, path, token, machine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if machine != "" {
		req.Header.Set("X-Wsq-Machine", machine)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// postPair : POST /v1/pair sur le handler (secret dans Authorization, identifiant de machine facultatif).
func postPair(h http.Handler, secret, machine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/v1/pair", strings.NewReader(body))
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if machine != "" {
		req.Header.Set("X-Wsq-Machine", machine)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// pairReplyOf : réponse 200 de POST /v1/pair décodée (jeton, hôte, version).
func pairReplyOf(t *testing.T, rec *httptest.ResponseRecorder) (token, host, version string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/pair : code %d, %s", rec.Code, rec.Body.String())
	}
	var v struct {
		Token   string `json:"token"`
		Host    string `json:"host"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	return v.Token, v.Host, v.Version
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
