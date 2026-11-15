package main

// Tests de la synchro automatique : route PUT /v1/saves/manager (déclaration du manager), commande
// --sync auto, et autoSyncAfterGame contre un faux manager en TLS (même contrat que l'agent).

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// managerBody : corps de PUT /v1/saves/manager valide (adresse, jeton et empreinte donnés).
func managerBody(t *testing.T, url, token, fp string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"name": "wsquashfs-manager", "url": url, "token": token, "fingerprint": fp,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// managersOf : pairs déclarés par le manager dans sync.json.
func managersOf(cfg syncConfig) []syncPeer {
	var out []syncPeer
	for _, p := range cfg.Peers {
		if p.Manager {
			out = append(out, p)
		}
	}
	return out
}

// TestAgentManagerPut : PUT /v1/saves/manager refuse les entrées mal formées (400 avec détail),
// déclare le manager (200), le remplace au second appel sans toucher aux autres machines, et
// /v1/info donne son empreinte et l'état de la synchro automatique.
func TestAgentManagerPut(t *testing.T) {
	_, h, _ := agentFixture(t)
	const machine = "client-machine-01"
	fp := strings.Repeat("ab", 32)
	if rec := doSync(h, "GET", "/v1/info", machine, nil); rec.Code != http.StatusOK {
		t.Fatalf("info : code %d", rec.Code)
	} else {
		var info map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		if info["manager"] != "" || info["auto"] != false {
			t.Errorf("sans manager : info %v", info)
		}
	}

	bad := map[string][]byte{
		"adresse en http":       managerBody(t, "http://10.0.0.2:8790", "manager-token", fp),
		"sans port":             managerBody(t, "https://10.0.0.2", "manager-token", fp),
		"avec un chemin":        managerBody(t, "https://10.0.0.2:8790/x", "manager-token", fp),
		"port hors plage":       managerBody(t, "https://10.0.0.2:99999", "manager-token", fp),
		"jeton vide":            managerBody(t, "https://10.0.0.2:8790", "", fp),
		"jeton avec espace":     managerBody(t, "https://10.0.0.2:8790", "a b", fp),
		"empreinte trop courte": managerBody(t, "https://10.0.0.2:8790", "manager-token", "abcd"),
		"empreinte non hex":     managerBody(t, "https://10.0.0.2:8790", "manager-token", strings.Repeat("g", 64)),
		"corps illisible":       []byte("pas du json"),
	}
	for name, body := range bad {
		rec := doSync(h, "PUT", "/v1/saves/manager", machine, body)
		if rec.Code != http.StatusBadRequest || detailOf(t, rec) == "" {
			t.Errorf("%s : code %d, attendu 400 avec détail", name, rec.Code)
		}
	}
	if rec := doSync(h, "PUT", "/v1/saves/manager", "", managerBody(t, "https://10.0.0.2:8790", "manager-token", fp)); rec.Code != http.StatusBadRequest {
		t.Errorf("sans en-tête X-Wsq-Machine : code %d, attendu 400", rec.Code)
	}
	if n := len(managersOf(loadSyncConfig())); n != 0 {
		t.Fatalf("entrées refusées : %d manager(s) déclaré(s)", n)
	}

	// Une machine associée à la main, qui ne doit pas être touchée par la déclaration.
	cfg := loadSyncConfig()
	cfg.Peers = []syncPeer{{Name: "steambox", URL: "https://10.0.0.3:8790", Token: "t", Fingerprint: strings.Repeat("CD", 32)}}
	if err := saveSyncConfig(cfg); err != nil {
		t.Fatal(err)
	}

	rec := doSync(h, "PUT", "/v1/saves/manager", machine, managerBody(t, "https://10.0.0.2:8790", "manager-token", fp))
	if rec.Code != http.StatusOK {
		t.Fatalf("déclaration : code %d (%s)", rec.Code, rec.Body.String())
	}
	var reply map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply["manager"] != strings.ToUpper(fp) || reply["auto"] != false {
		t.Errorf("réponse : %v", reply)
	}
	cfg = loadSyncConfig()
	mgr := managersOf(cfg)
	if len(mgr) != 1 || mgr[0].URL != "https://10.0.0.2:8790" || mgr[0].Token != "manager-token" || mgr[0].Fingerprint != strings.ToUpper(fp) {
		t.Fatalf("manager déclaré : %+v", mgr)
	}
	if len(cfg.Peers) != 2 {
		t.Errorf("machines : %+v, attendues la machine associée et le manager", cfg.Peers)
	}

	// Second appel : il remplace le manager, il n'y en a toujours qu'un.
	fp2 := strings.Repeat("ef", 32)
	if rec := doSync(h, "PUT", "/v1/saves/manager", machine, managerBody(t, "https://10.0.0.4:8790", "manager-token-2", fp2)); rec.Code != http.StatusOK {
		t.Fatalf("second appel : code %d", rec.Code)
	}
	cfg = loadSyncConfig()
	mgr = managersOf(cfg)
	if len(mgr) != 1 || mgr[0].URL != "https://10.0.0.4:8790" || mgr[0].Fingerprint != strings.ToUpper(fp2) {
		t.Fatalf("manager après remplacement : %+v", mgr)
	}
	if len(cfg.Peers) != 2 || cfg.Peers[0].Name != "steambox" {
		t.Errorf("machines après remplacement : %+v", cfg.Peers)
	}

	// /v1/info : empreinte du manager et état de la synchro automatique.
	cfg.Auto = true
	if err := saveSyncConfig(cfg); err != nil {
		t.Fatal(err)
	}
	rec = doSync(h, "GET", "/v1/info", machine, nil)
	var info map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info["manager"] != strings.ToUpper(fp2) || info["auto"] != true {
		t.Errorf("info : manager %v, auto %v", info["manager"], info["auto"])
	}
}

// TestSyncAutoRequiresManager : activer la synchro automatique sans manager déclaré est refusé ;
// avec un manager, elle s'active, et son état se lit dans --sync auto.
func TestSyncAutoRequiresManager(t *testing.T) {
	agentFixture(t)
	out, _ := capteUI(t)
	if _, err := syncAutoSet(loadSyncConfig(), true); err == nil || !strings.Contains(err.Error(), "aucun manager déclaré") {
		t.Fatalf("sans manager : erreur %v", err)
	}
	if rc := cmdSync([]string{"auto"}); rc != 0 || !strings.Contains(out.String(), "aucun manager déclaré") {
		t.Fatalf("état sans manager : code %d, sortie %q", rc, out.String())
	}

	cfg := loadSyncConfig()
	cfg.Peers = []syncPeer{{Name: "wsquashfs-manager", URL: "https://10.0.0.2:8790", Token: "manager-token", Fingerprint: strings.Repeat("AB", 32), Manager: true}}
	if err := saveSyncConfig(cfg); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if rc := cmdSync([]string{"auto", "on"}); rc != 0 || !loadSyncConfig().Auto {
		t.Fatalf("--sync auto on : code %d, active %v", rc, loadSyncConfig().Auto)
	}
	out.Reset()
	if rc := cmdSync([]string{"auto", "on"}); rc != 0 || !strings.Contains(out.String(), "déjà activée") || !loadSyncConfig().Auto {
		t.Errorf("deuxième on : code %d, sortie %q", rc, out.String())
	}
	out.Reset()
	if rc := cmdSync([]string{"auto"}); rc != 0 || !strings.Contains(out.String(), "wsquashfs-manager") || strings.Contains(out.String(), "inactive") {
		t.Errorf("état actif : code %d, sortie %q", rc, out.String())
	}
	out.Reset()
	if rc := cmdSync([]string{"auto", "off"}); rc != 0 || loadSyncConfig().Auto {
		t.Fatalf("--sync auto off : code %d, active %v", rc, loadSyncConfig().Auto)
	}
	out.Reset()
	if rc := cmdSync([]string{"auto", "off"}); rc != 0 || !strings.Contains(out.String(), "déjà désactivée") {
		t.Errorf("deuxième off : code %d, sortie %q", rc, out.String())
	}
}

// autoManager : faux manager en TLS, qui reçoit les archives de PUT /v1/saves/tar comme le fait le
// manager réel (jeton et machine vérifiés).
type autoManager struct {
	puts int      // archives reçues
	fps  []string // X-Wsq-Fingerprint de chaque envoi
	meta []string // « taille date » (X-Wsq-Size, X-Wsq-Newest) de chaque envoi
	body []byte   // dernière archive
}

// newAutoManager : serveur de test et le pair qui le désigne (empreinte du certificat).
func newAutoManager(t *testing.T) (*autoManager, syncPeer) {
	t.Helper()
	m := &autoManager{}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/saves/tar", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer manager-token" || r.Header.Get("X-Wsq-Machine") != "client-machine-01" {
			writeDetail(w, http.StatusUnauthorized, "jeton refusé")
			return
		}
		if r.URL.Query().Get("game") != "Game" {
			writeDetail(w, http.StatusBadRequest, "jeu inattendu")
			return
		}
		m.puts++
		m.fps = append(m.fps, r.Header.Get("X-Wsq-Fingerprint"))
		m.meta = append(m.meta, r.Header.Get("X-Wsq-Size")+" "+r.Header.Get("X-Wsq-Newest"))
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeDetail(w, http.StatusBadRequest, "corps illisible")
			return
		}
		m.body = body
		writeJSON(w, http.StatusOK, map[string]string{"fingerprint": "recu", "archived": "/backups/Game.tar.gz"})
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().Raw)
	return m, syncPeer{
		Name:        "wsquashfs-manager",
		URL:         srv.URL,
		Token:       "manager-token",
		Fingerprint: strings.ToUpper(hex.EncodeToString(sum[:])),
		Manager:     true,
	}
}

// autoConfig : sync.json de cette machine (identifiant client-machine-01) avec les pairs et l'état donnés.
func autoConfig(t *testing.T, auto bool, peers ...syncPeer) {
	t.Helper()
	if err := saveSyncConfig(syncConfig{ID: "client-machine-01", Peers: peers, Auto: auto}); err != nil {
		t.Fatal(err)
	}
}

// tarHas : vrai si l'archive tar.gz contient une entrée dont le nom contient file.
func tarHas(t *testing.T, body []byte, file string) bool {
	t.Helper()
	path := filepath.Join(t.TempDir(), "envoi.tar.gz")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("tar", "-tzf", path).CombinedOutput()
	if err != nil {
		t.Fatalf("archive illisible : %v (%s)", err, out)
	}
	return strings.Contains(string(out), file)
}

// TestAutoSyncAfterGame : à la sortie d'un jeu, la partie part au manager quand la synchro automatique
// est active, qu'elle a changé et que le manager répond. Rien sinon ; un manager injoignable ou
// refusant le certificat est signalé sans arrêter le lanceur.
func TestAutoSyncAfterGame(t *testing.T) {
	isolateSaves(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	syncGlobalsSaved(t)
	out, _ := capteUI(t)
	const game = "Game"
	users := filepath.Join(savesDir, game, "drive_c", "users")
	syncTestPopulate(t, users, "steamuser")
	target := gameTarget(game, users)
	m, peer := newAutoManager(t)

	// Synchro automatique désactivée : rien n'est envoyé, même manager déclaré.
	autoConfig(t, false, peer)
	autoSyncAfterGame(game)
	if m.puts != 0 {
		t.Fatalf("désactivée : %d envoi(s)", m.puts)
	}

	// Activée : la partie part, avec son empreinte ; l'état est enregistré.
	autoConfig(t, true, peer)
	want := target.fingerprint(target.files())
	out.Reset()
	autoSyncAfterGame(game)
	if m.puts != 1 || m.fps[0] != want {
		t.Fatalf("premier envoi : %d envoi(s), empreinte %v, attendue %s", m.puts, m.fps, want)
	}
	if !tarHas(t, m.body, "save1.sav") {
		t.Error("l'archive reçue ne contient pas la sauvegarde")
	}
	files := target.files()
	if wantMeta := fmt.Sprintf("%d %d", saveSize(files), newestSave(files)); m.meta[0] != wantMeta {
		t.Errorf("taille et date envoyées : %q, attendu %q", m.meta[0], wantMeta)
	}
	if !strings.Contains(out.String(), "Partie envoyée au manager (/backups/Game.tar.gz)") {
		t.Errorf("sortie : %q", out.String())
	}
	if s := loadSyncState(syncStateRoot(), syncPeerID(peer), game).Fingerprint; s != want {
		t.Errorf("état enregistré %q, attendu %s", s, want)
	}

	// Partie inchangée depuis l'envoi : rien.
	autoSyncAfterGame(game)
	if m.puts != 1 {
		t.Errorf("partie inchangée : %d envoi(s), attendu 1", m.puts)
	}

	// Partie modifiée : nouvel envoi.
	syncTestPut(t, filepath.Join(users, "steamuser", "Documents", "Game", "save1.sav"), "nouvelle partie", syncTestTime.Add(2*time.Hour))
	autoSyncAfterGame(game)
	if m.puts != 2 {
		t.Errorf("partie modifiée : %d envoi(s), attendu 2", m.puts)
	}

	// Manager injoignable (port fermé) : une note, sans panique ni sortie.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	down := peer
	down.URL = "https://" + closed
	down.Fingerprint = strings.Repeat("AB", 32)
	autoConfig(t, true, down)
	syncTestPut(t, filepath.Join(users, "steamuser", "Documents", "Game", "save1.sav"), "encore", syncTestTime.Add(3*time.Hour))
	out.Reset()
	autoSyncAfterGame(game)
	if !strings.Contains(out.String(), "Manager injoignable : partie gardée ici") || m.puts != 2 {
		t.Errorf("injoignable : sortie %q, envois %d", out.String(), m.puts)
	}

	// Empreinte de certificat différente : échec signalé, rien envoyé.
	wrong := peer
	wrong.Fingerprint = strings.Repeat("0", 64)
	autoConfig(t, true, wrong)
	out.Reset()
	autoSyncAfterGame(game)
	if !strings.Contains(out.String(), "empreinte du certificat différente") || m.puts != 2 {
		t.Errorf("mauvais certificat : sortie %q, envois %d", out.String(), m.puts)
	}

	// Jeu dont les sauvegardes sont hors du prefix (SAVEFILES, lu au lancement) : jamais envoyé.
	autoConfig(t, true, peer)
	oldFiles := savefilesKey
	t.Cleanup(func() { savefilesKey = oldFiles })
	savefilesKey = "drive_c/game/save.dat"
	autoSyncAfterGame(game)
	if m.puts != 2 {
		t.Errorf("SAVEFILES : %d envoi(s), attendu 2", m.puts)
	}
}
