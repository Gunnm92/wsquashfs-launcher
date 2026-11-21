package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wsquashfs-launcher/internal/agent"
	"wsquashfs-launcher/internal/savesync"
)

// syncRig : un agent « serveur » en TLS, et un client qui le vise.
type syncRig struct {
	srv     *httptest.Server
	h       http.Handler
	peer    savesync.Peer
	client  *savesync.Client
	users   string // users/ du serveur, jeu « Game »
	backups string // WSQUASHFS_BACKUP_DIR
	// lock : verrou d'un jeu de cette machine-ci (le client) ; absent : celui du lanceur.
	lock func(name string) (*os.File, bool)
}

// local : la synchro de la machine-cliente de la rig (même environnement que le serveur, qui tourne
// dans le même processus, sauf le verrou local).
func (r *syncRig) local() *savesync.Syncer {
	env := syncEnv()
	if r.lock != nil {
		env.LockIdleGame = r.lock
	}
	return savesync.New(env)
}

const syncRigGame = "Game"

// newSyncRig : agent de test (agentFixture) servi en TLS, savesDir/cacheDir isolés, client prêt.
func newSyncRig(t *testing.T) *syncRig {
	t.Helper()
	return newSyncRigWith(t, nil, nil)
}

// newSyncRigWith : newSyncRig, avec un réglage du serveur avant son démarrage (délais, handler) et un
// réglage de l'environnement du client (délais).
func newSyncRigWith(t *testing.T, setup func(*httptest.Server), tune func(*savesync.Env)) *syncRig {
	t.Helper()
	_, h, _ := agentFixture(t)
	isolateState(t)
	conf.savesDir, conf.cacheDir = t.TempDir(), t.TempDir()
	backups := t.TempDir()
	t.Setenv("WSQUASHFS_BACKUP_DIR", backups)

	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	if setup != nil {
		setup(srv)
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().Raw)
	peer := savesync.Peer{
		Name:        "steambox",
		URL:         srv.URL,
		Token:       "secret-token",
		Fingerprint: strings.ToUpper(hex.EncodeToString(sum[:])),
	}
	users := filepath.Join(conf.savesDir, syncRigGame, "drive_c", "users")
	syncTestPopulate(t, users, "steamuser")
	env := syncEnv()
	if tune != nil {
		tune(&env)
	}
	return &syncRig{
		srv:     srv,
		h:       h,
		peer:    peer,
		client:  savesync.New(env).NewClient(peer, "client-machine-01"),
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

// slowBody : corps de requête livré en deux morceaux ; pause est appelée entre les deux.
type slowBody struct {
	data  []byte
	sent  int
	pause func()
}

func (b *slowBody) Read(p []byte) (int, error) {
	half := len(b.data) / 2
	switch {
	case b.sent < half:
		n := copy(p, b.data[b.sent:half])
		b.sent += n
		return n, nil
	case b.sent == half && b.pause != nil:
		b.pause()
		b.pause = nil
	}
	if b.sent == len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.sent:])
	b.sent += n
	return n, nil
}

// syncTestSaveArchive : archive des sauvegardes d'une machine neuve (profil « deck »).
func syncTestSaveArchive(t *testing.T) []byte {
	t.Helper()
	local := clientUsers(t)
	syncTestPopulate(t, local, "deck")
	files := savesync.CollectSaveFiles(local)
	var buf bytes.Buffer
	if _, err := writeSaveArchive(&buf, local, files); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestSyncSendSlowBodyOutlivesReadTimeout : un envoi dont le corps dure plus que le ReadTimeout
// de l'agent (60 s en vrai, 1 s ici) aboutit : savesPut pose son propre délai de lecture
// (savesPutTimeout). Sans lui : 400 « archive refusée : … i/o timeout ».
func TestSyncSendSlowBodyOutlivesReadTimeout(t *testing.T) {
	r := newSyncRigWith(t, func(srv *httptest.Server) {
		base := agentSrv().HTTPServer(agent.Config{}, false)
		srv.Config.ReadHeaderTimeout = base.ReadHeaderTimeout
		srv.Config.ReadTimeout = time.Second
	}, nil)
	archive := syncTestSaveArchive(t)
	body := &slowBody{data: archive, pause: func() { time.Sleep(1500 * time.Millisecond) }}
	resp, err := r.client.Do(http.MethodPut, "/v1/saves/tar", url.Values{"game": {syncRigGame}}, body,
		map[string]string{"X-Wsq-Profile": "deck"})
	if err != nil {
		t.Fatalf("envoi lent refusé : %v", err)
	}
	resp.Body.Close()
	// Le profil du serveur (steamuser) a reçu le contenu envoyé.
	got, err := os.ReadFile(filepath.Join(r.users, "steamuser", "Documents", "Game", "save1.sav"))
	if err != nil || string(got) != "sauvegarde" {
		t.Errorf("sauvegarde reçue : %q, %v", got, err)
	}
}

// TestSyncFetchWaitsLongerForHeaders : l'agent prépare toute l'archive avant de répondre ; la
// réception attend ses en-têtes plus longtemps (syncFetchHeaderTimeout) que les autres requêtes,
// qui échouent toujours vite sur une machine qui ne répond pas (syncHeaderTimeout).
func TestSyncFetchWaitsLongerForHeaders(t *testing.T) {
	r := newSyncRigWith(t, func(srv *httptest.Server) {
		h := srv.Config.Handler
		srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			time.Sleep(800 * time.Millisecond) // machine lente : en-têtes tardifs
			h.ServeHTTP(w, req)
		})
	}, func(e *savesync.Env) { e.HeaderTimeout, e.FetchHeaderTimeout = 200*time.Millisecond, 30*time.Second })
	want := saveFingerprint(r.users, savesync.CollectSaveFiles(r.users))
	fp, err := r.client.Fetch(syncer().GameTarget(syncRigGame, clientUsers(t)))
	if err != nil {
		t.Fatalf("réception avec en-têtes tardifs : %v", err)
	}
	if fp != want {
		t.Errorf("empreinte reçue %s, attendue %s", fp, want)
	}
	if _, err := r.client.Info(); !savesync.IsUnreachable(err) {
		t.Errorf("info sur une machine lente : %v, attendu machine injoignable", err)
	}
	if _, err := r.client.List(); !savesync.IsUnreachable(err) {
		t.Errorf("list sur une machine lente : %v, attendu machine injoignable", err)
	}
}

// clientUsers : users/ d'un client vide (le dossier n'existe pas encore).
func clientUsers(t *testing.T) string {
	return filepath.Join(t.TempDir(), "users")
}

func TestSyncListStates(t *testing.T) {
	r := newSyncRig(t)
	// Jeu sans sauvegarde (profil vide) : absent de la liste.
	if err := os.MkdirAll(filepath.Join(conf.savesDir, "Vide", "drive_c", "users", "steamuser"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Jeu sans sauvegarde mais avec un état pour ce client : listé, empreinte vide.
	if err := savesync.SaveState(syncer().StateRoot(), "client-machine-01", "Ancien", "ABCD"); err != nil {
		t.Fatal(err)
	}
	games, err := r.client.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 {
		t.Fatalf("liste : %+v, attendu Ancien et Game", games)
	}
	if games[0].Name != "Ancien" || games[0].Fingerprint != "" || games[0].State != "ABCD" {
		t.Errorf("Ancien : %+v", games[0])
	}
	want := saveFingerprint(r.users, savesync.CollectSaveFiles(r.users))
	if games[1].Name != syncRigGame || games[1].Fingerprint != want || games[1].State != "" || games[1].Newest == 0 {
		t.Errorf("Game : %+v, empreinte attendue %s", games[1], want)
	}
	if size := savesync.SaveSize(savesync.CollectSaveFiles(r.users)); games[1].Size == 0 || games[1].Size != size {
		t.Errorf("taille Game : %d, attendue %d", games[1].Size, size)
	}
}

func TestSyncFetchThenAck(t *testing.T) {
	r := newSyncRig(t)
	want := saveFingerprint(r.users, savesync.CollectSaveFiles(r.users))
	local := clientUsers(t)
	fp, err := r.client.Fetch(syncer().GameTarget(syncRigGame, local))
	if err != nil {
		t.Fatal(err)
	}
	if fp != want {
		t.Fatalf("empreinte reçue %s, attendue %s", fp, want)
	}
	if got := saveFingerprint(local, savesync.CollectSaveFiles(local)); got != want {
		t.Errorf("empreinte locale après réception %s, attendue %s", got, want)
	}
	if err := r.client.Ack(syncRigGame, fp); err != nil {
		t.Fatal(err)
	}
	if st := savesync.LoadState(syncer().StateRoot(), "client-machine-01", syncRigGame); st.Fingerprint != want {
		t.Errorf("état serveur %q, attendu %s", st.Fingerprint, want)
	}
	games, err := r.client.List()
	if err != nil {
		t.Fatal(err)
	}
	if games[len(games)-1].State != want {
		t.Errorf("liste : état %q, attendu %s", games[len(games)-1].State, want)
	}
}

func TestSyncAckRefusedWhenServerChanged(t *testing.T) {
	r := newSyncRig(t)
	if err := r.client.Ack(syncRigGame, "0000"); err == nil {
		t.Fatal("ack avec une mauvaise empreinte accepté")
	}
	if st := savesync.LoadState(syncer().StateRoot(), "client-machine-01", syncRigGame); st.Fingerprint != "" {
		t.Errorf("état écrit malgré le refus : %q", st.Fingerprint)
	}
}

// TestSyncSendReplacesRemote : après une réception (état commun), le client change ses sauvegardes
// et les envoie : le serveur remplace son périmètre, rien n'est mis de côté (il n'a pas changé).
func TestSyncSendReplacesRemote(t *testing.T) {
	r := newSyncRig(t)
	local := clientUsers(t)
	fp, err := r.client.Fetch(syncer().GameTarget(syncRigGame, local))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.client.Ack(syncRigGame, fp); err != nil {
		t.Fatal(err)
	}
	syncTestPut(t, filepath.Join(local, "steamuser", "Documents", "Game", "nouvelle.sav"), "nouveau", syncTestTime.Add(time.Hour))
	mine := saveFingerprint(local, savesync.CollectSaveFiles(local))
	remote, archived, err := r.client.Send(syncer().GameTarget(syncRigGame, local))
	if err != nil {
		t.Fatal(err)
	}
	if archived != "" {
		t.Errorf("archive créée alors que le serveur n'a pas changé : %s", archived)
	}
	if remote != mine {
		t.Errorf("empreinte distante %s, attendue %s", remote, mine)
	}
	if got := saveFingerprint(r.users, savesync.CollectSaveFiles(r.users)); got != mine {
		t.Errorf("serveur : empreinte %s, attendue %s", got, mine)
	}
	if _, err := os.Stat(filepath.Join(r.users, "steamuser", "Documents", "Game", "nouvelle.sav")); err != nil {
		t.Errorf("fichier envoyé absent côté serveur : %v", err)
	}
	if st := savesync.LoadState(syncer().StateRoot(), "client-machine-01", syncRigGame); st.Fingerprint != mine {
		t.Errorf("état serveur %q, attendu %s", st.Fingerprint, mine)
	}
}

// TestSyncSendArchivesRemoteChange : le serveur a changé depuis la dernière synchro : sa version est
// mise de côté dans WSQUASHFS_BACKUP_DIR avant d'être remplacée.
func TestSyncSendArchivesRemoteChange(t *testing.T) {
	r := newSyncRig(t)
	local := clientUsers(t)
	fp, err := r.client.Fetch(syncer().GameTarget(syncRigGame, local))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.client.Ack(syncRigGame, fp); err != nil {
		t.Fatal(err)
	}
	// Changement côté serveur seul, après la synchro.
	syncTestPut(t, filepath.Join(r.users, "steamuser", "Documents", "Game", "serveur-seul.sav"), "serveur", syncTestTime)
	// Changement côté client.
	syncTestPut(t, filepath.Join(local, "steamuser", "Documents", "Game", "client.sav"), "client", syncTestTime)

	_, archived, err := r.client.Send(syncer().GameTarget(syncRigGame, local))
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
	_, err := syncer().NewClient(peer, "client-machine-01").List()
	if err == nil || !strings.Contains(err.Error(), "empreinte du certificat différente") {
		t.Fatalf("mauvaise empreinte : erreur %v", err)
	}
	if savesync.IsUnreachable(err) {
		t.Error("empreinte fausse classée « injoignable »")
	}
}

func TestSyncBadToken(t *testing.T) {
	r := newSyncRig(t)
	peer := r.peer
	peer.Token = "mauvais"
	_, err := syncer().NewClient(peer, "client-machine-01").List()
	if err == nil || !strings.Contains(err.Error(), "jeton refusé") {
		t.Fatalf("mauvais jeton : erreur %v", err)
	}
}

// TestSyncBusyGame : jeu verrouillé sur la machine distante : 409, savesync.ErrBusy pour réception et envoi.
func TestSyncBusyGame(t *testing.T) {
	r := newSyncRig(t)
	lock, ok := lockIdleGame(syncRigGame)
	if !ok {
		t.Fatal("verrou de test impossible")
	}
	defer lock.Close()
	if _, err := r.client.Fetch(syncer().GameTarget(syncRigGame, clientUsers(t))); !errors.Is(err, savesync.ErrBusy) {
		t.Errorf("réception jeu verrouillé : erreur %v", err)
	}
	local := clientUsers(t)
	syncTestPopulate(t, local, "steamuser")
	if _, _, err := r.client.Send(syncer().GameTarget(syncRigGame, local)); !errors.Is(err, savesync.ErrBusy) {
		t.Errorf("envoi jeu verrouillé : erreur %v", err)
	}
	if got := saveFingerprint(r.users, savesync.CollectSaveFiles(r.users)); got == "" {
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
	before := saveFingerprint(r.users, savesync.CollectSaveFiles(r.users))
	if rec := doSync(r.h, "PUT", "/v1/saves/tar?game="+syncRigGame, "client-machine-01", []byte("pas une archive")); rec.Code != http.StatusBadRequest {
		t.Errorf("archive illisible : code %d", rec.Code)
	}
	if got := saveFingerprint(r.users, savesync.CollectSaveFiles(r.users)); got != before {
		t.Error("archive illisible : serveur modifié")
	}
	// Corps trop grand : 413 (limite abaissée pour le test).
	oldMax := syncMaxBody
	t.Cleanup(func() { syncMaxBody = oldMax })
	syncMaxBody = 64
	var buf bytes.Buffer
	if _, err := writeSaveArchive(&buf, r.users, savesync.CollectSaveFiles(r.users)); err != nil {
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
	// Identifiant présent mais qui n'est pas celui de la machine du jeton : refusé par l'authentification
	// (401), avant la validation de l'identifiant.
	if rec := doSync(r.h, "GET", "/v1/saves/list", "pas valide/x", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("identifiant invalide : code %d, attendu 401", rec.Code)
	}
	if rec := doSync(r.h, "GET", "/v1/saves/list", "autre-machine-02", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("identifiant d'une autre machine : code %d, attendu 401", rec.Code)
	}
}

func TestSyncServerClosedIsUnreachable(t *testing.T) {
	r := newSyncRig(t)
	r.srv.Close()
	_, err := r.client.List()
	if err == nil || !savesync.IsUnreachable(err) {
		t.Fatalf("serveur arrêté : erreur %v, attendue « injoignable »", err)
	}
}

// TestSyncGamesListsCopyModes : un jeu seulement en copie de travail (cacheDir/wine, marquée) et un
// jeu seulement en mise de côté (copyUsersStash) sont dans syncer().Names() et dans /v1/saves/list, comme
// le jeu de l'overlay : même liste des deux côtés.
func TestSyncGamesListsCopyModes(t *testing.T) {
	r := newSyncRig(t)
	const wineGame, stashGame = "Wine Game", "Stash Game"
	wine := filepath.Join(conf.cacheDir, "wine", wineGame)
	syncTestPopulate(t, filepath.Join(wine, "drive_c", "users"), "steamuser")
	syncTestPut(t, filepath.Join(wine, ".wsquashfs_cache"), "sig", syncTestTime)
	syncTestPopulate(t, copyUsersStash(stashGame), "steamuser")

	want := []string{syncRigGame, stashGame, wineGame}
	if got := syncer().Names(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Names : %v, attendu %v", got, want)
	}
	rec := doSync(r.h, http.MethodGet, "/v1/saves/list", "client-machine-01", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/saves/list : code %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Games []savesync.RemoteSave `json:"games"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	var names []string
	for _, g := range got.Games {
		if g.Fingerprint == "" {
			t.Errorf("%s listé sans empreinte", g.Name)
		}
		if g.Content == "" {
			t.Errorf("%s listé sans empreinte de contenu", g.Name)
		}
		names = append(names, g.Name)
	}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("/v1/saves/list : %v, attendu %v", names, want)
	}
}

// deleteSaves : DELETE /v1/saves/tar pour le jeu de la rig, machine donnée.
func deleteSaves(t *testing.T, r *syncRig, game, machine string) (int, map[string]any) {
	t.Helper()
	rec := doSync(r.h, http.MethodDelete, "/v1/saves/tar?game="+game, machine, nil)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// TestSyncDeleteSaves : suppression par l'agent. Archive dans backups d'abord, fichiers du périmètre
// effacés, cache et fichier hors périmètre conservés, état de la machine appelante effacé et
// seul celui-ci : l'état d'une autre machine reste.
func TestSyncDeleteSaves(t *testing.T) {
	r := newSyncRig(t)
	// Hors périmètre : un cache dans un dossier du périmètre, et un fichier hors saveProfileDirs.
	cache := filepath.Join(r.users, "steamuser", "Documents", "shadercache_x", "s.bin")
	hors := filepath.Join(r.users, "steamuser", "Pictures", "p.png")
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("cache de test absent : %v", err)
	}
	if err := savesync.SaveState(syncer().StateRoot(), "client-machine-01", syncRigGame, "AAAA"); err != nil {
		t.Fatal(err)
	}
	if err := savesync.SaveState(syncer().StateRoot(), "autre-machine-02", syncRigGame, "BBBB"); err != nil {
		t.Fatal(err)
	}
	code, body := deleteSaves(t, r, syncRigGame, "client-machine-01")
	if code != http.StatusOK {
		t.Fatalf("DELETE : code %d, %v", code, body)
	}
	archived, _ := body["archived"].(string)
	if archived == "" || filepath.Dir(archived) != r.backups {
		t.Fatalf("archive %q hors de WSQUASHFS_BACKUP_DIR", archived)
	}
	if out, err := exec.Command("tar", "-tf", archived).CombinedOutput(); err != nil || !strings.Contains(string(out), "save1.sav") {
		t.Errorf("archive sans les sauvegardes (%v) : %s", err, out)
	}
	if n, _ := body["removed"].(float64); int(n) != 2 {
		t.Errorf("removed %v, attendu 2 (save1.sav, cfg.ini ; caches, lien et profil système exclus)", body["removed"])
	}
	for _, p := range []string{
		filepath.Join(r.users, "steamuser", "Documents", "Game", "save1.sav"),
		filepath.Join(r.users, "steamuser", "AppData", "Local", "Game", "cfg.ini"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("fichier du périmètre encore présent : %s (%v)", p, err)
		}
	}
	// Dossier devenu vide : retiré jusqu'à users/steamuser, qui reste.
	if _, err := os.Stat(filepath.Join(r.users, "steamuser", "AppData", "Local", "Game")); !os.IsNotExist(err) {
		t.Error("dossier vide AppData/Local/Game non retiré")
	}
	if _, err := os.Stat(filepath.Join(r.users, "steamuser")); err != nil {
		t.Errorf("profil supprimé : %v", err)
	}
	for _, keep := range []string{cache, hors} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("fichier hors périmètre supprimé : %s (%v)", keep, err)
		}
	}
	if st := savesync.LoadState(syncer().StateRoot(), "client-machine-01", syncRigGame); st.Fingerprint != "" {
		t.Errorf("état de la machine appelante conservé : %q", st.Fingerprint)
	}
	if st := savesync.LoadState(syncer().StateRoot(), "autre-machine-02", syncRigGame); st.Fingerprint != "BBBB" {
		t.Errorf("état d'une autre machine modifié : %q", st.Fingerprint)
	}
	// Pour cette machine, le jeu n'a plus ni sauvegarde ni état : il ne figure plus dans la liste.
	games, err := r.client.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range games {
		if g.Name == syncRigGame {
			t.Errorf("jeu encore listé après suppression : %+v", g)
		}
	}
}

// TestSyncDeleteNoSaves : jeu sans sauvegarde : 200, rien archivé ni supprimé, état de la machine
// appelante effacé.
func TestSyncDeleteNoSaves(t *testing.T) {
	r := newSyncRig(t)
	if err := os.RemoveAll(filepath.Join(r.users, "steamuser")); err != nil {
		t.Fatal(err)
	}
	if err := savesync.SaveState(syncer().StateRoot(), "client-machine-01", syncRigGame, "AAAA"); err != nil {
		t.Fatal(err)
	}
	code, body := deleteSaves(t, r, syncRigGame, "client-machine-01")
	if code != http.StatusOK || body["removed"] != float64(0) || body["archived"] != "" {
		t.Errorf("jeu sans sauvegarde : code %d, %v", code, body)
	}
	if entries, _ := os.ReadDir(r.backups); len(entries) != 0 {
		t.Errorf("archive créée sans sauvegarde : %d entrée(s)", len(entries))
	}
	if st := savesync.LoadState(syncer().StateRoot(), "client-machine-01", syncRigGame); st.Fingerprint != "" {
		t.Errorf("état conservé : %q", st.Fingerprint)
	}
}

func TestSyncDeleteRefusals(t *testing.T) {
	r := newSyncRig(t)
	if code, _ := deleteSaves(t, r, "..%2Fx", "client-machine-01"); code != http.StatusBadRequest {
		t.Errorf("jeu invalide : code %d, attendu 400", code)
	}
	lock, ok := lockIdleGame(syncRigGame)
	if !ok {
		t.Fatal("verrou de test impossible")
	}
	defer lock.Close()
	if code, _ := deleteSaves(t, r, syncRigGame, "client-machine-01"); code != http.StatusConflict {
		t.Errorf("jeu en cours : code %d, attendu 409", code)
	}
	if got := saveFingerprint(r.users, savesync.CollectSaveFiles(r.users)); got == "" {
		t.Error("sauvegardes supprimées malgré le verrou")
	}
	if rec := doSync(r.h, http.MethodDelete, "/v1/saves/tar?game="+syncRigGame, "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("sans X-Wsq-Machine : code %d, attendu 400", rec.Code)
	}
}

// TestSyncDeleteArchiveFailureKeepsSaves : archivage impossible (dossier backups inutilisable) :
// 500 et rien n'est supprimé.
func TestSyncDeleteArchiveFailureKeepsSaves(t *testing.T) {
	r := newSyncRig(t)
	blocker := filepath.Join(t.TempDir(), "fichier")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WSQUASHFS_BACKUP_DIR", filepath.Join(blocker, "sous"))
	before := saveFingerprint(r.users, savesync.CollectSaveFiles(r.users))
	if code, _ := deleteSaves(t, r, syncRigGame, "client-machine-01"); code != http.StatusInternalServerError {
		t.Fatalf("archivage en échec : code %d, attendu 500", code)
	}
	if got := saveFingerprint(r.users, savesync.CollectSaveFiles(r.users)); got != before {
		t.Error("sauvegardes supprimées alors que l'archivage a échoué")
	}
}

// TestSyncPairTooOldLauncher : l'autre machine n'a pas POST /v1/pair (404) : « lanceur trop ancien »,
// et non une erreur de code.
func TestSyncPairTooOldLauncher(t *testing.T) {
	r := newSyncRigWith(t, func(srv *httptest.Server) {
		h := srv.Config.Handler
		srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/v1/pair" {
				http.NotFound(w, req)
				return
			}
			h.ServeHTTP(w, req)
		})
	}, nil)
	peer := r.peer
	peer.Token = "secret-peu-importe"
	if _, err := syncer().NewClient(peer, "client-machine-01").Pair(); !errors.Is(err, savesync.ErrPairOld) {
		t.Errorf("lanceur ancien : erreur %v, attendu savesync.ErrPairOld", err)
	}
}

// TestSyncPutArchivesLocalWithoutState : première synchro avec ce manager (aucun état pour cette
// machine) : le manager pousse son archive, la copie locale est mise de côté (backups) avant d'être
// remplacée. Une fois l'état enregistré, une nouvelle réception sans changement local n'archive rien.
func TestSyncPutArchivesLocalWithoutState(t *testing.T) {
	r := newSyncRig(t)
	if err := os.Remove(filepath.Join(syncer().StateRoot(), "client-machine-01", syncRigGame+".json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	rec := doSync(r.h, http.MethodPut, "/v1/saves/tar?game="+syncRigGame, "client-machine-01", syncTestSaveArchive(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT : code %d, %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	archived, _ := body["archived"].(string)
	if archived == "" || filepath.Dir(archived) != r.backups {
		t.Fatalf("copie locale non archivée (archived %q)", archived)
	}
	if out, err := exec.Command("tar", "-tf", archived).CombinedOutput(); err != nil || !strings.Contains(string(out), "save1.sav") {
		t.Errorf("archive sans la copie locale (%v) : %s", err, out)
	}
	rec = doSync(r.h, http.MethodPut, "/v1/saves/tar?game="+syncRigGame, "client-machine-01", syncTestSaveArchive(t))
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["archived"] != "" {
		t.Errorf("deuxième envoi : archive %v, attendu aucune (%v)", body["archived"], err)
	}
}
