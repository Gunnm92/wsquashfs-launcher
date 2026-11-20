package main

// Concordance des routes /v1/saves/* de l'agent et du client de --sync avec contract/agent-api.yaml :
// ce que le client envoie à un pair (requêtes) et ce que l'agent répond (réponses) doivent être dans
// le contrat. Le reste des routes de l'agent est dans internal/agent/contract_test.go.

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wsquashfs-launcher/internal/agent"
	"wsquashfs-launcher/internal/contracttest"
	"wsquashfs-launcher/internal/savesync"
)

// contractHandler : le handler, avec contrôle de chaque échange. checkReq : la requête elle-même doit
// être dans le contrat (faux pour les appels qui visent un refus).
func contractHandler(t *testing.T, c *contracttest.Contract, h http.Handler, checkReq bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		target := r.URL.RequestURI()
		if checkReq {
			c.CheckRequest(t, r.Method, target, r.Header, body)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		c.CheckResponse(t, r.Method, target, rec.Code, rec.Header(), rec.Body.Bytes(), false)
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

// TestContractSyncClientRequests : ce que --sync envoie à un pair (liste, réception, envoi, état,
// info) est dans le contrat, et les réponses de l'agent aussi.
func TestContractSyncClientRequests(t *testing.T) {
	c := contracttest.Load(t)
	r := newSyncRigWith(t, func(srv *httptest.Server) {
		srv.Config.Handler = contractHandler(t, c, srv.Config.Handler, true)
	}, nil)
	if _, err := r.client.Info(); err != nil {
		t.Fatal(err)
	}
	games, err := r.client.List()
	if err != nil || len(games) == 0 {
		t.Fatalf("liste : %v %v", games, err)
	}
	local := clientUsers(t)
	target := syncer().GameTarget(syncRigGame, local)
	fp, err := r.client.Fetch(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.client.Ack(syncRigGame, fp); err != nil {
		t.Fatal(err)
	}
	syncTestPut(t, filepath.Join(local, "steamuser", "Documents", "Game", "nouvelle.sav"), "nouveau", syncTestTime.Add(3600e9))
	if _, _, err := r.client.Send(target); err != nil {
		t.Fatal(err)
	}
}

// TestContractSavesRoutes : les routes /v1/saves/* de l'agent, y compris leurs refus.
func TestContractSavesRoutes(t *testing.T) {
	c := contracttest.Load(t)
	_, base, _ := agentFixture(t)
	isolateSaves(t)
	t.Setenv("WSQUASHFS_EDEN_DIR", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(os.Getenv("HOME"), "data"))
	t.Setenv("WSQUASHFS_BACKUP_DIR", t.TempDir())
	users := filepath.Join(conf.savesDir, syncRigGame, "drive_c", "users")
	syncTestPopulate(t, users, "steamuser")

	ok := contractHandler(t, c, base, true)
	refused := contractHandler(t, c, base, false)
	const machine = "client-machine-01"
	call := func(h http.Handler, method, target, mach string, body []byte, want int) *httptest.ResponseRecorder {
		t.Helper()
		rec := doSync(h, method, target, mach, body)
		if rec.Code != want {
			t.Fatalf("%s %s : code %d, attendu %d : %s", method, target, rec.Code, want, rec.Body.String())
		}
		return rec
	}

	// Liste.
	call(ok, "GET", "/v1/saves/list", machine, nil, 200)
	call(refused, "GET", "/v1/saves/list", "", nil, 400)
	call(refused, "GET", "/v1/saves/list", "pas valide !", nil, 401) // présent : doit être celui du jeton

	// Réception.
	tar := call(ok, "GET", "/v1/saves/tar?game="+syncRigGame, machine, nil, 200)
	fp := tar.Header().Get("X-Wsq-Fingerprint")
	call(ok, "GET", "/v1/saves/tar?game=Inconnu", machine, nil, 404)
	call(ok, "GET", "/v1/saves/tar?game=dossier:inconnu", machine, nil, 404)
	call(refused, "GET", "/v1/saves/tar?game=../x", machine, nil, 400)

	// État commun.
	state := func(game, fingerprint string) []byte {
		return jsonBody(map[string]string{"game": game, "fingerprint": fingerprint})
	}
	call(ok, "POST", "/v1/saves/state", machine, state(syncRigGame, fp), 200)
	call(ok, "POST", "/v1/saves/state", machine, state(syncRigGame, "0000"), 409)
	call(ok, "POST", "/v1/saves/state", machine, state("dossier:inconnu", fp), 404)
	call(refused, "POST", "/v1/saves/state", machine, state("../x", fp), 400)
	call(refused, "POST", "/v1/saves/state", machine, []byte("pas du json"), 400)

	// Envoi : archive valide, archive refusée, dossier non déclaré.
	call(ok, "PUT", "/v1/saves/tar?game="+syncRigGame, machine, syncTestSaveArchive(t), 200)
	call(refused, "PUT", "/v1/saves/tar?game="+syncRigGame, machine, []byte("pas une archive"), 400)
	call(refused, "PUT", "/v1/saves/tar?game=dossier:inconnu", machine, plainArchive(t, writeDir(t)), 404)

	// Un jeu en cours répond 409 `jeu en cours`.
	lock, locked := lockIdleGame(syncRigGame)
	if !locked {
		t.Fatal("verrou de test impossible")
	}
	call(ok, "GET", "/v1/saves/tar?game="+syncRigGame, machine, nil, 409)
	call(ok, "PUT", "/v1/saves/tar?game="+syncRigGame, machine, syncTestSaveArchive(t), 409)
	call(ok, "DELETE", "/v1/saves/tar?game="+syncRigGame, machine, nil, 409)
	lock.Close()

	// Suppression, puis sans rien à supprimer.
	call(ok, "DELETE", "/v1/saves/tar?game="+syncRigGame, machine, nil, 200)
	call(ok, "DELETE", "/v1/saves/tar?game="+syncRigGame, machine, nil, 200)
	call(ok, "DELETE", "/v1/saves/tar?game=dossier:inconnu", machine, nil, 200)
	call(refused, "DELETE", "/v1/saves/tar?game=../x", machine, nil, 400)

	// Dossiers déclarés.
	call(ok, "GET", "/v1/saves/dirs", machine, nil, 200)
	call(ok, "PUT", "/v1/saves/dirs", machine, jsonBody(map[string]string{"name": "hookshot", "path": ".local/share/hookshot/save"}), 200)
	call(refused, "PUT", "/v1/saves/dirs", machine, jsonBody(map[string]string{"name": "etc", "path": "/etc"}), 400)
	call(ok, "GET", "/v1/saves/dirs", machine, nil, 200)
	call(ok, "DELETE", "/v1/saves/dirs?name=hookshot", machine, nil, 200)
	call(ok, "DELETE", "/v1/saves/dirs?name=hookshot", machine, nil, 404)

	// Déclaration du manager.
	good := map[string]string{"name": "wsquashfs-manager (hote)", "url": "https://10.0.0.2:8765", "token": "jeton-manager_1", "fingerprint": strings.Repeat("ab", 32), "ui": "http://10.0.0.2:8000"}
	call(ok, "PUT", "/v1/saves/manager", machine, jsonBody(good), 200)
	call(ok, "GET", "/v1/info", machine, nil, 200)
	good["url"] = "http://10.0.0.2:8765"
	call(refused, "PUT", "/v1/saves/manager", machine, jsonBody(good), 400)
	call(refused, "GET", "/v1/saves/dirs", "", nil, 400)
}

// TestContractEventBodies : ce que le lanceur envoie à POST /v1/events (début et fin de
// partie) est dans le contrat.
func TestContractEventBodies(t *testing.T) {
	c := contracttest.Load(t)
	code := 0
	for _, ev := range []gameEvent{
		{Event: "start", Game: "Crazy Taxi 3", At: 1700000000, Controllers: []agent.Controller{{Kind: "xbox", Name: "Pad", Bus: "usb"}}},
		{Event: "start", Game: "Sans manette", At: 1700000000},
		{Event: "exit", Game: "Crazy Taxi 3", At: 1700003600, Started: 1700000000, Code: &code},
	} {
		c.CheckRequest(t, "POST", "/v1/events", http.Header{"X-Wsq-Machine": {"client-machine-01"}}, jsonBody(ev))
	}
}

func jsonBody(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// writeDir : dossier de sauvegardes simple, avec un fichier.
func writeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"slot.sav": "progression"})
	return dir
}

// TestContractNotifyManagerSendsEvents : NotifyManager poste vers /v1/events, comme le contrat le
// décrit (en-têtes, corps) ; le manager (faux) répond avec le corps du contrat.
func TestContractNotifyManagerSendsEvents(t *testing.T) {
	c := contracttest.Load(t)
	agentFixture(t)
	isolateSaves(t)
	var got []string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.CheckRequest(t, r.Method, r.URL.RequestURI(), r.Header, body)
		got = append(got, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
		c.CheckResponse(t, r.Method, r.URL.RequestURI(), 200, w.Header(), []byte(`{"ok": true}`), true)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().Raw)
	cfg := savesync.LoadConfig()
	cfg.Peers = []savesync.Peer{{Name: "manager", URL: srv.URL, Token: "jeton", Manager: true,
		Fingerprint: strings.ToUpper(hex.EncodeToString(sum[:]))}}
	if err := savesync.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	code := 1
	syncer().NotifyManager(gameEvent{Event: "start", Game: "Jeu", At: 1700000000, Controllers: []agent.Controller{{Kind: "xbox", Name: "Pad", Bus: "usb"}}})
	syncer().NotifyManager(gameEvent{Event: "exit", Game: "Jeu", At: 1700000100, Started: 1700000000, Code: &code})
	if len(got) != 2 {
		t.Fatalf("événements reçus : %d", len(got))
	}
}
