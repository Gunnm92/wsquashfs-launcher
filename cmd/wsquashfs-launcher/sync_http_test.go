package main

// Tests de la synchro HTTPS (--sync) : client (sync_http.go) contre les routes /v1/saves/* de
// l'agent (agent_http.go), en TLS réel (httptest). Deux machines sont simulées : le serveur
// utilise les globales savesDir/cacheDir, le client des dossiers users explicites.

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// syncRig : un agent « serveur » en TLS, et un client qui le vise.
type syncRig struct {
	srv     *httptest.Server
	h       http.Handler
	peer    syncPeer
	client  *syncClient
	users   string // users/ du serveur, jeu « Game »
	backups string // WSQUASHFS_BACKUP_DIR
}

const syncRigGame = "Game"

// newSyncRig : agent de test (agentFixture) servi en TLS, savesDir/cacheDir isolés, client prêt.
func newSyncRig(t *testing.T) *syncRig {
	t.Helper()
	_, h, _ := agentFixture(t)
	oldSaves, oldCache := savesDir, cacheDir
	t.Cleanup(func() { savesDir, cacheDir = oldSaves, oldCache })
	savesDir, cacheDir = t.TempDir(), t.TempDir()
	backups := t.TempDir()
	t.Setenv("WSQUASHFS_BACKUP_DIR", backups)

	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().Raw)
	peer := syncPeer{
		Name:        "steambox",
		URL:         srv.URL,
		Token:       "secret-token",
		Fingerprint: strings.ToUpper(hex.EncodeToString(sum[:])),
	}
	users := filepath.Join(savesDir, syncRigGame, "drive_c", "users")
	syncTestPopulate(t, users, "steamuser")
	return &syncRig{
		srv:     srv,
		h:       h,
		peer:    peer,
		client:  newSyncClient(peer, "client-machine-01"),
		users:   users,
		backups: backups,
	}
}

// doSync : requête directe au handler (jeton valide, en-tête X-Wsq-Machine si machine != "").
func doSync(h http.Handler, method, target, machine string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-token")
	if machine != "" {
		req.Header.Set("X-Wsq-Machine", machine)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// clientUsers : users/ d'un client vide (le dossier n'existe pas encore).
func clientUsers(t *testing.T) string {
	return filepath.Join(t.TempDir(), "users")
}

func TestSyncListStates(t *testing.T) {
	r := newSyncRig(t)
	// Jeu sans sauvegarde (profil vide) : absent de la liste.
	if err := os.MkdirAll(filepath.Join(savesDir, "Vide", "drive_c", "users", "steamuser"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Jeu sans sauvegarde mais avec un état pour ce client : listé, empreinte vide.
	if err := saveSyncState(syncStateRoot(), "client-machine-01", "Ancien", "ABCD"); err != nil {
		t.Fatal(err)
	}
	games, err := r.client.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 {
		t.Fatalf("liste : %+v, attendu Ancien et Game", games)
	}
	if games[0].Name != "Ancien" || games[0].Fingerprint != "" || games[0].State != "ABCD" {
		t.Errorf("Ancien : %+v", games[0])
	}
	want := saveFingerprint(r.users, collectSaveFiles(r.users))
	if games[1].Name != syncRigGame || games[1].Fingerprint != want || games[1].State != "" || games[1].Newest == 0 {
		t.Errorf("Game : %+v, empreinte attendue %s", games[1], want)
	}
	if size := saveSize(collectSaveFiles(r.users)); games[1].Size == 0 || games[1].Size != size {
		t.Errorf("taille Game : %d, attendue %d", games[1].Size, size)
	}
}

func TestSyncFetchThenAck(t *testing.T) {
	r := newSyncRig(t)
	want := saveFingerprint(r.users, collectSaveFiles(r.users))
	local := clientUsers(t)
	fp, err := r.client.fetch(syncRigGame, local)
	if err != nil {
		t.Fatal(err)
	}
	if fp != want {
		t.Fatalf("empreinte reçue %s, attendue %s", fp, want)
	}
	if got := saveFingerprint(local, collectSaveFiles(local)); got != want {
		t.Errorf("empreinte locale après réception %s, attendue %s", got, want)
	}
	if err := r.client.ack(syncRigGame, fp); err != nil {
		t.Fatal(err)
	}
	if st := loadSyncState(syncStateRoot(), "client-machine-01", syncRigGame); st.Fingerprint != want {
		t.Errorf("état serveur %q, attendu %s", st.Fingerprint, want)
	}
	games, err := r.client.list()
	if err != nil {
		t.Fatal(err)
	}
	if games[len(games)-1].State != want {
		t.Errorf("liste : état %q, attendu %s", games[len(games)-1].State, want)
	}
}

func TestSyncAckRefusedWhenServerChanged(t *testing.T) {
	r := newSyncRig(t)
	if err := r.client.ack(syncRigGame, "0000"); err == nil {
		t.Fatal("ack avec une mauvaise empreinte accepté")
	}
	if st := loadSyncState(syncStateRoot(), "client-machine-01", syncRigGame); st.Fingerprint != "" {
		t.Errorf("état écrit malgré le refus : %q", st.Fingerprint)
	}
}

// TestSyncSendReplacesRemote : après une réception (état commun), le client change ses sauvegardes
// et les envoie : le serveur remplace son périmètre, rien n'est mis de côté (il n'a pas changé).
func TestSyncSendReplacesRemote(t *testing.T) {
	r := newSyncRig(t)
	local := clientUsers(t)
	fp, err := r.client.fetch(syncRigGame, local)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.client.ack(syncRigGame, fp); err != nil {
		t.Fatal(err)
	}
	syncTestPut(t, filepath.Join(local, "steamuser", "Documents", "Game", "nouvelle.sav"), "nouveau", syncTestTime.Add(time.Hour))
	mine := saveFingerprint(local, collectSaveFiles(local))
	remote, archived, err := r.client.send(syncRigGame, local)
	if err != nil {
		t.Fatal(err)
	}
	if archived != "" {
		t.Errorf("archive créée alors que le serveur n'a pas changé : %s", archived)
	}
	if remote != mine {
		t.Errorf("empreinte distante %s, attendue %s", remote, mine)
	}
	if got := saveFingerprint(r.users, collectSaveFiles(r.users)); got != mine {
		t.Errorf("serveur : empreinte %s, attendue %s", got, mine)
	}
	if _, err := os.Stat(filepath.Join(r.users, "steamuser", "Documents", "Game", "nouvelle.sav")); err != nil {
		t.Errorf("fichier envoyé absent côté serveur : %v", err)
	}
	if st := loadSyncState(syncStateRoot(), "client-machine-01", syncRigGame); st.Fingerprint != mine {
		t.Errorf("état serveur %q, attendu %s", st.Fingerprint, mine)
	}
}

// TestSyncSendArchivesRemoteChange : le serveur a changé depuis la dernière synchro : sa version est
// mise de côté dans WSQUASHFS_BACKUP_DIR avant d'être remplacée.
func TestSyncSendArchivesRemoteChange(t *testing.T) {
	r := newSyncRig(t)
	local := clientUsers(t)
	fp, err := r.client.fetch(syncRigGame, local)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.client.ack(syncRigGame, fp); err != nil {
		t.Fatal(err)
	}
	// Changement côté serveur seul, après la synchro.
	syncTestPut(t, filepath.Join(r.users, "steamuser", "Documents", "Game", "serveur-seul.sav"), "serveur", syncTestTime)
	// Changement côté client.
	syncTestPut(t, filepath.Join(local, "steamuser", "Documents", "Game", "client.sav"), "client", syncTestTime)

	_, archived, err := r.client.send(syncRigGame, local)
	if err != nil {
		t.Fatal(err)
	}
	if archived == "" {
		t.Fatal("version distante changée non mise de côté")
	}
	if filepath.Dir(archived) != r.backups {
		t.Errorf("archive hors de WSQUASHFS_BACKUP_DIR : %s", archived)
	}
	if out, err := exec.Command("tar", "-tf", archived).CombinedOutput(); err != nil || !strings.Contains(string(out), "serveur-seul.sav") {
		t.Errorf("archive sans la version perdue (%v) : %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(r.users, "steamuser", "Documents", "Game", "serveur-seul.sav")); !os.IsNotExist(err) {
		t.Error("fichier du serveur encore présent après remplacement")
	}
}

func TestSyncBadCertificateFingerprint(t *testing.T) {
	r := newSyncRig(t)
	peer := r.peer
	peer.Fingerprint = strings.Repeat("0", 64)
	_, err := newSyncClient(peer, "client-machine-01").list()
	if err == nil || !strings.Contains(err.Error(), "empreinte du certificat différente") {
		t.Fatalf("mauvaise empreinte : erreur %v", err)
	}
	if isUnreachable(err) {
		t.Error("empreinte fausse classée « injoignable »")
	}
}

func TestSyncBadToken(t *testing.T) {
	r := newSyncRig(t)
	peer := r.peer
	peer.Token = "mauvais"
	_, err := newSyncClient(peer, "client-machine-01").list()
	if err == nil || !strings.Contains(err.Error(), "jeton refusé") {
		t.Fatalf("mauvais jeton : erreur %v", err)
	}
}

// TestSyncBusyGame : jeu verrouillé sur la machine distante : 409, errSyncBusy pour réception et envoi.
func TestSyncBusyGame(t *testing.T) {
	r := newSyncRig(t)
	lock, ok := lockIdleGame(syncRigGame)
	if !ok {
		t.Fatal("verrou de test impossible")
	}
	defer lock.Close()
	if _, err := r.client.fetch(syncRigGame, clientUsers(t)); !errors.Is(err, errSyncBusy) {
		t.Errorf("réception jeu verrouillé : erreur %v", err)
	}
	local := clientUsers(t)
	syncTestPopulate(t, local, "steamuser")
	if _, _, err := r.client.send(syncRigGame, local); !errors.Is(err, errSyncBusy) {
		t.Errorf("envoi jeu verrouillé : erreur %v", err)
	}
	if got := saveFingerprint(r.users, collectSaveFiles(r.users)); got == "" {
		t.Error("serveur modifié malgré le verrou")
	}
}

func TestSyncRouteValidation(t *testing.T) {
	r := newSyncRig(t)
	bad := "..%2Fx"
	if rec := doSync(r.h, "GET", "/v1/saves/tar?game="+bad, "client-machine-01", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("GET jeu ../x : code %d", rec.Code)
	}
	if rec := doSync(r.h, "PUT", "/v1/saves/tar?game="+bad, "client-machine-01", []byte("x")); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT jeu ../x : code %d", rec.Code)
	}
	if rec := doSync(r.h, "GET", "/v1/saves/tar?game=Absent", "client-machine-01", nil); rec.Code != http.StatusNotFound {
		t.Errorf("jeu sans sauvegarde : code %d", rec.Code)
	}
	// Archive illisible : refusée, rien remplacé.
	before := saveFingerprint(r.users, collectSaveFiles(r.users))
	if rec := doSync(r.h, "PUT", "/v1/saves/tar?game="+syncRigGame, "client-machine-01", []byte("pas une archive")); rec.Code != http.StatusBadRequest {
		t.Errorf("archive illisible : code %d", rec.Code)
	}
	if got := saveFingerprint(r.users, collectSaveFiles(r.users)); got != before {
		t.Error("archive illisible : serveur modifié")
	}
	// Corps trop grand : 413 (limite abaissée pour le test).
	oldMax := syncMaxBody
	t.Cleanup(func() { syncMaxBody = oldMax })
	syncMaxBody = 64
	var buf bytes.Buffer
	if _, err := writeSaveArchive(&buf, r.users, collectSaveFiles(r.users)); err != nil {
		t.Fatal(err)
	}
	if rec := doSync(r.h, "PUT", "/v1/saves/tar?game="+syncRigGame, "client-machine-01", buf.Bytes()); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("corps trop grand : code %d, attendu 413", rec.Code)
	}
}

func TestSyncMachineHeaderRequired(t *testing.T) {
	r := newSyncRig(t)
	for _, c := range []struct{ method, target string }{
		{"GET", "/v1/saves/list"},
		{"GET", "/v1/saves/tar?game=" + syncRigGame},
		{"PUT", "/v1/saves/tar?game=" + syncRigGame},
		{"POST", "/v1/saves/state"},
	} {
		if rec := doSync(r.h, c.method, c.target, "", []byte(`{}`)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s sans X-Wsq-Machine : code %d, attendu 400", c.method, c.target, rec.Code)
		}
	}
	if rec := doSync(r.h, "GET", "/v1/saves/list", "pas valide/x", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("identifiant invalide : code %d", rec.Code)
	}
}

func TestSyncServerClosedIsUnreachable(t *testing.T) {
	r := newSyncRig(t)
	r.srv.Close()
	_, err := r.client.list()
	if err == nil || !isUnreachable(err) {
		t.Fatalf("serveur arrêté : erreur %v, attendue « injoignable »", err)
	}
}

func TestParsePairCode(t *testing.T) {
	fp := strings.Repeat("ab", 32)
	p, err := parsePairCode("wsq://tok_EN-1@192.168.1.20:8790#" + fp)
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "https://192.168.1.20:8790" || p.Token != "tok_EN-1" || p.Fingerprint != strings.ToUpper(fp) || p.Name != "" {
		t.Errorf("code valide : %+v", p)
	}
	p, err = parsePairCode("wsq://tok@[fd00::20]:8790#" + fp)
	if err != nil {
		t.Fatalf("IPv6 refusée : %v", err)
	}
	if p.URL != "https://[fd00::20]:8790" {
		t.Errorf("IPv6 : URL %q", p.URL)
	}
	for _, code := range []string{
		"http://tok@192.168.1.20:8790#" + fp,           // mauvais schéma
		"wsq://tok@192.168.1.20:8790#" + fp[:63],       // empreinte trop courte
		"wsq://tok@192.168.1.20:8790#" + "zz" + fp[2:], // empreinte non hexadécimale
		"wsq://192.168.1.20:8790#" + fp,                // sans jeton
		"wsq://tok@192.168.1.20:8790",                  // sans empreinte
		"wsq://tok@192.168.1.20#" + fp,                 // sans port
		"wsq://tok@192.168.1.20:0#" + fp,               // port hors limites
		"wsq://tok@192.168.1.20:99999#" + fp,           // port hors limites
		"wsq://to k@192.168.1.20:8790#" + fp,           // jeton mal formé
	} {
		if _, err := parsePairCode(code); err == nil {
			t.Errorf("code accepté : %q", code)
		}
	}
}

func TestSyncConfigRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := loadSyncConfig()
	if !validSyncID(cfg.ID) {
		t.Fatalf("identifiant généré invalide : %q", cfg.ID)
	}
	if pathExistsFile(syncConfigPath()) {
		t.Fatal("loadSyncConfig a écrit le fichier")
	}
	cfg.Peers = []syncPeer{{Name: "aibox", URL: "https://10.0.0.2:8790", Token: "t", Fingerprint: "F"}}
	if err := saveSyncConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(syncConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("droits %v, attendu 600", st.Mode().Perm())
	}
	again := loadSyncConfig()
	if again.ID != cfg.ID || len(again.Peers) != 1 || again.Peers[0].Name != "aibox" {
		t.Errorf("relecture : %+v", again)
	}
}

// TestSyncGamesListsCopyModes : un jeu seulement en copie de travail (cacheDir/wine, marquée) et un
// jeu seulement en mise de côté (copyUsersStash) sont dans syncGames() et dans /v1/saves/list, comme
// le jeu de l'overlay : même liste des deux côtés.
func TestSyncGamesListsCopyModes(t *testing.T) {
	r := newSyncRig(t)
	const wineGame, stashGame = "Wine Game", "Stash Game"
	wine := filepath.Join(cacheDir, "wine", wineGame)
	syncTestPopulate(t, filepath.Join(wine, "drive_c", "users"), "steamuser")
	syncTestPut(t, filepath.Join(wine, ".wsquashfs_cache"), "sig", syncTestTime)
	syncTestPopulate(t, copyUsersStash(stashGame), "steamuser")

	want := []string{syncRigGame, stashGame, wineGame}
	if got := syncGames(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("syncGames : %v, attendu %v", got, want)
	}
	rec := doSync(r.h, http.MethodGet, "/v1/saves/list", "client-machine-01", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/saves/list : code %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Games []remoteSave `json:"games"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	var names []string
	for _, g := range got.Games {
		if g.Fingerprint == "" {
			t.Errorf("%s listé sans empreinte", g.Name)
		}
		names = append(names, g.Name)
	}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("/v1/saves/list : %v, attendu %v", names, want)
	}
}
