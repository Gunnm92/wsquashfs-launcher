package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"wsquashfs-launcher/internal/savesync"
)

// isolateSaves : savesDir et cacheDir temporaires (l'état de synchro est écrit sous savesDir/.sync).
// isolateState : conf, launch et layers retrouvent à la fin du test leur valeur d'avant.
func isolateState(t *testing.T) {
	t.Helper()
	oldConf, oldLaunch, oldLayers := conf, launch, layers
	t.Cleanup(func() { conf, launch, layers = oldConf, oldLaunch, oldLayers })
}

func isolateSaves(t *testing.T) {
	t.Helper()
	isolateState(t)
	conf.savesDir, conf.cacheDir = t.TempDir(), t.TempDir()
}

// TestAgentSyncDirsRoutes : le manager déclare un dossier par l'agent ; il apparaît dans la liste avec
// son nom lisible, s'envoie et se reçoit comme un jeu ; DELETE retire la déclaration sans effacer.
func TestAgentSyncDirsRoutes(t *testing.T) {
	_, h, _ := agentFixture(t)
	isolateSaves(t)
	t.Setenv("WSQUASHFS_EDEN_DIR", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(os.Getenv("HOME"), "data"))
	home, _ := filepath.EvalSymlinks(os.Getenv("HOME"))
	dir := filepath.Join(home, ".local", "share", "hookshot", "save")
	writeTree(t, dir, map[string]string{"slot.sav": "progression"})

	body, _ := json.Marshal(savesync.Dir{Name: "hookshot", Path: ".local/share/hookshot/save"})
	if rec := doSync(h, "PUT", "/v1/saves/dirs", "client-machine-01", body); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"path":".local/share/hookshot/save"`) {
		t.Fatalf("PUT dirs : %d %s", rec.Code, rec.Body.String())
	}
	// Un dossier déclaré mais vide n'est pas listé : la machine est « absente » pour lui.
	empty, _ := json.Marshal(savesync.Dir{Name: "vide", Path: "vide"})
	if rec := doSync(h, "PUT", "/v1/saves/dirs", "client-machine-01", empty); rec.Code != http.StatusOK {
		t.Fatalf("PUT dirs vide : %d %s", rec.Code, rec.Body.String())
	}
	bad, _ := json.Marshal(savesync.Dir{Name: "etc", Path: "/etc"})
	if rec := doSync(h, "PUT", "/v1/saves/dirs", "client-machine-01", bad); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT hors HOME : %d", rec.Code)
	}
	rec := doSync(h, "GET", "/v1/saves/list", "client-machine-01", nil)
	var list struct{ Games []savesync.RemoteSave }
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	found := false
	for _, g := range list.Games {
		if g.Name == "dossier:hookshot" && g.Label == "hookshot" && g.Fingerprint != "" {
			found = true
		}
	}
	if !found || strings.Contains(rec.Body.String(), "dossier:vide") {
		t.Fatalf("liste : %s", rec.Body.String())
	}
	if got := strings.Join(syncer().Names(), ","); got != "dossier:hookshot" {
		t.Errorf("entrées locales (--sync) : %q", got)
	}
	// Réception : la version locale (jamais synchronisée avec cette machine) est archivée d'abord.
	other := t.TempDir()
	writeTree(t, other, map[string]string{"slot.sav": "autre progression, plus longue"})
	rec = doSync(h, "PUT", "/v1/saves/tar?game=dossier:hookshot", "client-machine-01", plainArchive(t, other))
	var put struct{ Fingerprint, Archived string }
	_ = json.Unmarshal(rec.Body.Bytes(), &put)
	if rec.Code != http.StatusOK || put.Archived == "" {
		t.Fatalf("PUT tar : %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "slot.sav")); string(got) != "autre progression, plus longue" {
		t.Errorf("fichier reçu : %q", got)
	}
	if rec := doSync(h, "GET", "/v1/saves/tar?game=dossier:hookshot", "client-machine-01", nil); rec.Code != http.StatusOK || rec.Header().Get("X-Wsq-Profile") != "" {
		t.Errorf("GET tar : %d, profil %q", rec.Code, rec.Header().Get("X-Wsq-Profile"))
	}
	if rec := doSync(h, "PUT", "/v1/saves/tar?game=dossier:inconnu", "client-machine-01", plainArchive(t, other)); rec.Code != http.StatusNotFound {
		t.Errorf("dossier non déclaré : %d", rec.Code)
	}
	if rec := doSync(h, "DELETE", "/v1/saves/dirs?name=hookshot", "client-machine-01", nil); rec.Code != http.StatusOK {
		t.Errorf("DELETE dirs : %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "slot.sav")); err != nil {
		t.Errorf("fichiers effacés avec la déclaration : %v", err)
	}
}

// TestAgentSwitchBusyWhileEdenRuns : tant qu'Eden tourne, une sauvegarde Switch n'est ni lue ni
// remplacée (409).
func TestAgentSwitchBusyWhileEdenRuns(t *testing.T) {
	_, h, _ := agentFixture(t)
	isolateSaves(t)
	edenFixture(t)
	eden := fakeEdenProcess(t)
	if rec := doSync(h, "GET", "/v1/saves/tar?game=switch:0100F2C0115B6000", "client-machine-01", nil); rec.Code != http.StatusConflict {
		t.Errorf("GET pendant Eden : %d", rec.Code)
	}
	_ = eden.Process.Kill()
	_, _ = eden.Process.Wait()
	if rec := doSync(h, "GET", "/v1/saves/tar?game=switch:0100F2C0115B6000", "client-machine-01", nil); rec.Code != http.StatusOK {
		t.Errorf("GET Eden arrêté : %d %s", rec.Code, rec.Body.String())
	}
}

// TestDeclaredDirPropagates : un dossier déclaré sur une machine se propage comme une sauvegarde : la
// liste donne son chemin (à partir de HOME), l'envoi le transmet (X-Wsq-Dir) et une machine qui ne l'a
// pas déclaré le déclare à la réception, sous le même nom. Sans chemin : refusé comme avant ; chemin
// hors de HOME : refusé.
func TestDeclaredDirPropagates(t *testing.T) {
	_, h, _ := agentFixture(t)
	isolateSaves(t)
	t.Setenv("WSQUASHFS_EDEN_DIR", "")
	home, _ := filepath.EvalSymlinks(os.Getenv("HOME"))
	src := t.TempDir()
	writeTree(t, src, map[string]string{"slot.sav": "zelda"})
	archive := plainArchive(t, src)
	name := "dossier:Zelda Ocarina Of Time"
	put := func(dir string) int {
		req := httptest.NewRequest("PUT", "/v1/saves/tar?game="+url.QueryEscape(name), bytes.NewReader(archive))
		req.Header.Set("Authorization", "Bearer secret-token")
		req.Header.Set("X-Wsq-Machine", "client-machine-01")
		if dir != "" {
			req.Header.Set(savesync.DirHeader, url.PathEscape(dir))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := put(""); code != http.StatusNotFound {
		t.Errorf("sans chemin : %d, attendu 404", code)
	}
	if code := put("../ailleurs"); code != http.StatusBadRequest {
		t.Errorf("chemin hors de HOME : %d, attendu 400", code)
	}
	if code := put(".local/share/hookshot/save"); code != http.StatusOK {
		t.Fatalf("avec chemin : %d", code)
	}
	if got, _ := os.ReadFile(filepath.Join(home, ".local", "share", "hookshot", "save", "slot.sav")); string(got) != "zelda" {
		t.Errorf("fichier reçu : %q", got)
	}
	dirs := savesync.LoadConfig().Dirs
	if len(dirs) != 1 || dirs[0].Name != "Zelda Ocarina Of Time" {
		t.Fatalf("déclaration : %+v", dirs)
	}
	rec := doSync(h, "GET", "/v1/saves/list", "client-machine-01", nil)
	if !strings.Contains(rec.Body.String(), `"dir":".local/share/hookshot/save"`) {
		t.Errorf("liste sans le chemin : %s", rec.Body.String())
	}
	// L'envoi d'un dossier déclaré transmet son chemin.
	var got string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(savesync.DirHeader)
		writeJSON(w, http.StatusOK, map[string]string{"fingerprint": "x"})
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().Raw)
	peer := savesync.Peer{Name: "b", URL: srv.URL, Token: "t", Fingerprint: strings.ToUpper(hex.EncodeToString(sum[:]))}
	tg, err := syncer().TargetOf(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := syncer().NewClient(peer, "client-machine-01").Send(tg); err != nil {
		t.Fatal(err)
	}
	if dec, _ := url.PathUnescape(got); dec != ".local/share/hookshot/save" {
		t.Errorf("en-tête %s : %q", savesync.DirHeader, got)
	}
}

// fakeEdenProcess : un processus dont le nom (comm) est « eden » : copie de sleep sous ce nom. Arrêté
// à la fin du test.
func fakeEdenProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep introuvable")
	}
	data, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "eden")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd
}
