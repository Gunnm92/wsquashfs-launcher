package main

// Tests de l'installation des images (games_http.go, routes /v1/games/*) : détection du dossier,
// règle HOME, envoi reprenable (PUT, commit), suppression. Appels directs au handler de l'agent
// (agentFixture, comme agent_test.go) ; HOME et XDG_CONFIG_HOME isolés dans t.TempDir().

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// gamesRig : agent de test (HOME isolé, roms/ avec une image), cacheDir et savesDir isolés.
type gamesRig struct {
	t    *testing.T
	s    *agentServer
	h    http.Handler
	home string // HOME (liens résolus)
}

func newGamesRig(t *testing.T) *gamesRig {
	t.Helper()
	s, h, _ := agentFixture(t)
	oldSaves, oldCache := savesDir, cacheDir
	t.Cleanup(func() { savesDir, cacheDir = oldSaves, oldCache })
	savesDir, cacheDir = t.TempDir(), t.TempDir()
	home, err := filepath.EvalSymlinks(os.Getenv("HOME"))
	if err != nil {
		t.Fatal(err)
	}
	return &gamesRig{t: t, s: s, h: h, home: home}
}

// req : requête directe au handler, jeton valide.
func (g *gamesRig) req(method, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-token")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	g.h.ServeHTTP(rec, req)
	return rec
}

// setDir : crée le dossier sous HOME et le choisit (POST /v1/games/dir), 200 attendu.
func (g *gamesRig) setDir(rel string) string {
	g.t.Helper()
	dir := filepath.Join(g.home, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		g.t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"dir": dir})
	rec := g.req("POST", "/v1/games/dir", body, nil)
	if rec.Code != http.StatusOK {
		g.t.Fatalf("POST /v1/games/dir %s : %d %s", dir, rec.Code, rec.Body.String())
	}
	return dir
}

// putPart : PUT d'un morceau (offset, taille totale annoncée).
func (g *gamesRig) putPart(name string, offset, total int64, body []byte) *httptest.ResponseRecorder {
	target := "/v1/games/file?name=" + url.QueryEscape(name) + "&offset=" + strconv.FormatInt(offset, 10)
	return g.req("PUT", target, body, map[string]string{"X-Wsq-Total-Size": strconv.FormatInt(total, 10)})
}

func (g *gamesRig) commit(name string, size int64, sum string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"name": name, "size": size, "sha256": sum})
	return g.req("POST", "/v1/games/commit", body, nil)
}

func decodeGames(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("réponse illisible : %s (%v)", rec.Body.String(), err)
	}
	return v
}

func sumHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// mkImages : dossier (créé) avec n images .wsquashfs vides, directement dedans.
func mkImages(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, "jeu"+strings.Repeat("x", i)+".wsquashfs")
		if err := os.WriteFile(p, []byte("hsqs"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetectGamesDir(t *testing.T) {
	// home : dossier HOME de test, sans liens (EvalSymlinks) ; nouveau pour chaque cas.
	newHome := func(t *testing.T) string {
		h, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	t.Run("le dossier le plus garni gagne", func(t *testing.T) {
		home := newHome(t)
		mkImages(t, filepath.Join(home, "A"), 2)
		mkImages(t, filepath.Join(home, "B"), 3)
		if got := detectGamesDir(home, nil); got != filepath.Join(home, "B") {
			t.Errorf("détecté %s", got)
		}
	})
	t.Run("égalité : Games/non-steam", func(t *testing.T) {
		home := newHome(t)
		mkImages(t, filepath.Join(home, "Alpha"), 2)
		mkImages(t, filepath.Join(home, "Games", "non-steam"), 2)
		if got := detectGamesDir(home, nil); got != filepath.Join(home, "Games", "non-steam") {
			t.Errorf("détecté %s", got)
		}
	})
	t.Run("égalité sans Games/non-steam : ordre alphabétique", func(t *testing.T) {
		home := newHome(t)
		mkImages(t, filepath.Join(home, "Beta"), 1)
		mkImages(t, filepath.Join(home, "Alpha"), 1)
		if got := detectGamesDir(home, nil); got != filepath.Join(home, "Alpha") {
			t.Errorf("détecté %s", got)
		}
	})
	t.Run("aucune image : HOME/Games/non-steam", func(t *testing.T) {
		home := newHome(t)
		mkImages(t, filepath.Join(home, "Vide"), 0)
		if got := detectGamesDir(home, nil); got != filepath.Join(home, "Games", "non-steam") {
			t.Errorf("détecté %s", got)
		}
	})
	t.Run("dossier caché ignoré", func(t *testing.T) {
		home := newHome(t)
		mkImages(t, filepath.Join(home, ".cache", "x"), 5)
		mkImages(t, filepath.Join(home, "Visible"), 1)
		if got := detectGamesDir(home, nil); got != filepath.Join(home, "Visible") {
			t.Errorf("détecté %s", got)
		}
	})
	t.Run("lien symbolique non suivi", func(t *testing.T) {
		home := newHome(t)
		outside := newHome(t)
		mkImages(t, outside, 5)
		if err := os.Symlink(outside, filepath.Join(home, "lien")); err != nil {
			t.Fatal(err)
		}
		mkImages(t, filepath.Join(home, "Reel"), 1)
		if got := detectGamesDir(home, nil); got != filepath.Join(home, "Reel") {
			t.Errorf("détecté %s (le lien ou sa cible ont été suivis)", got)
		}
	})
	t.Run("profondeur : 4 niveaux sous HOME au plus", func(t *testing.T) {
		home := newHome(t)
		mkImages(t, filepath.Join(home, "a", "b", "c", "d", "e"), 5)
		mkImages(t, filepath.Join(home, "Reel"), 1)
		if got := detectGamesDir(home, nil); got != filepath.Join(home, "Reel") {
			t.Errorf("détecté %s", got)
		}
	})
	t.Run("dossier roms hors HOME ignoré", func(t *testing.T) {
		home := newHome(t)
		roms := newHome(t)
		mkImages(t, roms, 5)
		mkImages(t, filepath.Join(home, "Reel"), 1)
		if got := detectGamesDir(home, []string{roms}); got != filepath.Join(home, "Reel") {
			t.Errorf("détecté %s", got)
		}
	})
	t.Run("dossier roms sous HOME au-delà de 4 niveaux pris en compte", func(t *testing.T) {
		home := newHome(t)
		roms := filepath.Join(home, "r", "s", "t", "u", "v")
		mkImages(t, filepath.Join(roms, "x"), 3)
		mkImages(t, filepath.Join(home, "Reel"), 1)
		if got := detectGamesDir(home, []string{roms}); got != filepath.Join(roms, "x") {
			t.Errorf("détecté %s", got)
		}
	})
	t.Run("roms : 3 niveaux sous le dossier roms au plus", func(t *testing.T) {
		home := newHome(t)
		roms := filepath.Join(home, "r", "s", "t", "u", "v")
		mkImages(t, filepath.Join(roms, "a", "b", "c", "d"), 5)
		mkImages(t, filepath.Join(home, "Reel"), 1)
		if got := detectGamesDir(home, []string{roms}); got != filepath.Join(home, "Reel") {
			t.Errorf("détecté %s", got)
		}
	})
}

func TestGamesDirPost(t *testing.T) {
	g := newGamesRig(t)
	sub := g.setDir("Jeux")
	cfg := loadAgentConfig()
	if cfg.GamesDir != sub {
		t.Errorf("choix non persisté : %q, attendu %q", cfg.GamesDir, sub)
	}
	if g.s.cfg.GamesDir != sub {
		t.Errorf("config en mémoire : %q", g.s.cfg.GamesDir)
	}
	rec := g.req("GET", "/v1/info", nil, nil)
	info := decodeGames(t, rec)
	if info["games_dir"] != sub || info["games_dir_set"] != true {
		t.Errorf("/v1/info : %v", info)
	}

	outside := t.TempDir()
	link := filepath.Join(g.home, "lien")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"/usr/local", outside, link, g.home, "Relatif"} {
		body, _ := json.Marshal(map[string]string{"dir": dir})
		if rec := g.req("POST", "/v1/games/dir", body, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %q : code %d, attendu 400 (%s)", dir, rec.Code, rec.Body.String())
		}
	}
	// Refus : le choix précédent reste en place.
	if cfg := loadAgentConfig(); cfg.GamesDir != sub {
		t.Errorf("choix modifié malgré le refus : %q", cfg.GamesDir)
	}

	// "" : retour à la détection, choix effacé et persistant.
	rec = g.req("POST", "/v1/games/dir", []byte(`{"dir":""}`), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retour détection : %d %s", rec.Code, rec.Body.String())
	}
	if out := decodeGames(t, rec); out["set"] != false {
		t.Errorf("set après retour à la détection : %v", out)
	}
	if cfg := loadAgentConfig(); cfg.GamesDir != "" {
		t.Errorf("games_dir non effacé : %q", cfg.GamesDir)
	}
}

func TestGamesInstallFlow(t *testing.T) {
	g := newGamesRig(t)
	dir := g.setDir("Games/non-steam")
	image := bytes.Repeat([]byte("wsquashfs-test-"), 700)
	n := int64(len(image))
	half := len(image) / 2
	name := "Jeu.wsquashfs"

	if rec := g.req("GET", "/v1/games/part?name="+name, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("part initial : %d", rec.Code)
	} else if p := decodeGames(t, rec); p["part_size"] != float64(0) || p["installed"] != false {
		t.Errorf("part initial : %v", p)
	}

	rec := g.putPart(name, 0, n, image[:half])
	if rec.Code != http.StatusOK || decodeGames(t, rec)["part_size"] != float64(half) {
		t.Fatalf("PUT 1 : %d %s", rec.Code, rec.Body.String())
	}
	rec = g.putPart(name, int64(half), n, image[half:])
	if rec.Code != http.StatusOK || decodeGames(t, rec)["part_size"] != float64(n) {
		t.Fatalf("PUT 2 : %d %s", rec.Code, rec.Body.String())
	}
	rec = g.commit(name, n, sumHex(image))
	if rec.Code != http.StatusOK {
		t.Fatalf("commit : %d %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || !bytes.Equal(got, image) {
		t.Fatalf("image installée absente ou différente (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name+".part")); !os.IsNotExist(err) {
		t.Errorf(".part encore présent après commit : %v", err)
	}

	rec = g.req("GET", "/v1/games/list", nil, nil)
	list := decodeGames(t, rec)
	games, _ := list["games"].([]any)
	if len(games) != 1 || list["dir"] != dir || list["set"] != true || len(list["parts"].([]any)) != 0 {
		t.Fatalf("liste : %v", list)
	}
	if first := games[0].(map[string]any); first["name"] != name || first["size"] != float64(n) {
		t.Errorf("entrée de liste : %v", first)
	}
	if free, _ := list["free"].(float64); free <= 0 {
		t.Errorf("espace libre : %v", list["free"])
	}

	if rec := g.req("GET", "/v1/games/part?name="+name, nil, nil); decodeGames(t, rec)["installed"] != true {
		t.Errorf("part après installation : %s", rec.Body.String())
	}
	if rec := g.putPart(name, 0, n, image); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "déjà installé") {
		t.Errorf("réinstallation : %d %s", rec.Code, rec.Body.String())
	}
}

func TestGamesResumeAndRefusals(t *testing.T) {
	g := newGamesRig(t)
	dir := g.setDir("Games/non-steam")
	image := bytes.Repeat([]byte("reprise-"), 1000)
	n := int64(len(image))
	name := "Reprise.wsquashfs"
	part := filepath.Join(dir, name+".part")

	// Commit sans envoi : 404.
	if rec := g.commit(name, n, sumHex(image)); rec.Code != http.StatusNotFound {
		t.Errorf("commit sans envoi : %d", rec.Code)
	}
	// Coupure : le corps s'arrête avant la taille annoncée, ce qui est reçu reste.
	if rec := g.putPart(name, 0, n, image[:300]); rec.Code != http.StatusOK || decodeGames(t, rec)["part_size"] != float64(300) {
		t.Fatalf("PUT partiel : %d %s", rec.Code, rec.Body.String())
	}
	if rec := g.req("GET", "/v1/games/part?name="+name, nil, nil); decodeGames(t, rec)["part_size"] != float64(300) {
		t.Errorf("part après coupure : %s", rec.Body.String())
	}
	// Mauvais décalage : 409 avec la taille à reprendre.
	for _, off := range []int64{0, 100, 301} {
		rec := g.putPart(name, off, n, image[off:])
		out := decodeGames(t, rec)
		if rec.Code != http.StatusConflict || out["detail"] != "décalage" || out["part_size"] != float64(300) {
			t.Errorf("décalage %d : %d %s", off, rec.Code, rec.Body.String())
		}
	}
	// Reprise au bon décalage.
	if rec := g.putPart(name, 300, n, image[300:]); rec.Code != http.StatusOK || decodeGames(t, rec)["part_size"] != float64(n) {
		t.Fatalf("reprise : %d %s", rec.Code, rec.Body.String())
	}
	// Taille annoncée au commit fausse : 409, rien supprimé.
	if rec := g.commit(name, n-1, sumHex(image)); rec.Code != http.StatusConflict || decodeGames(t, rec)["detail"] != "taille" {
		t.Errorf("commit taille fausse : %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(part); err != nil {
		t.Errorf(".part supprimé par un commit de taille fausse : %v", err)
	}
	// Empreinte fausse : 422, .part supprimé.
	if rec := g.commit(name, n, strings.Repeat("0", 64)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("commit mauvais sha : %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf(".part encore présent après empreinte fausse : %v", err)
	}
	if rec := g.req("GET", "/v1/games/part?name="+name, nil, nil); decodeGames(t, rec)["part_size"] != float64(0) {
		t.Errorf("part non vide après empreinte fausse : %s", rec.Body.String())
	}
	// Empreinte mal formée : 400.
	if rec := g.commit(name, n, "pas-une-empreinte"); rec.Code != http.StatusBadRequest {
		t.Errorf("empreinte mal formée : %d", rec.Code)
	}

	// Espace insuffisant : 507, sans rien écrire.
	if rec := g.putPart("Gros.wsquashfs", 0, 1<<60, []byte("x")); rec.Code != http.StatusInsufficientStorage || !strings.Contains(rec.Body.String(), "espace insuffisant") {
		t.Errorf("espace insuffisant : %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "Gros.wsquashfs.part")); !os.IsNotExist(err) {
		t.Errorf("fichier créé malgré l'espace insuffisant : %v", err)
	}

	// Noms refusés : 400 partout.
	for _, bad := range []string{"../x.wsquashfs", "x.exe", "sous/dossier.wsquashfs", ".caché.wsquashfs", "Jeu.wsquashfs.wsquashfs", ""} {
		if rec := g.putPart(bad, 0, 4, []byte("hsqs")); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT nom %q : %d, attendu 400", bad, rec.Code)
		}
		if rec := g.commit(bad, 4, sumHex([]byte("hsqs"))); rec.Code != http.StatusBadRequest {
			t.Errorf("commit nom %q : %d, attendu 400", bad, rec.Code)
		}
	}
	// Décalage ou taille totale invalides : 400.
	if rec := g.putPart(name, n+1, n, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("décalage au-delà du total : %d", rec.Code)
	}
	if rec := g.req("PUT", "/v1/games/file?name="+name+"&offset=0", []byte("x"), nil); rec.Code != http.StatusBadRequest {
		t.Errorf("sans taille totale : %d", rec.Code)
	}
	// Un seul envoi à la fois par nom.
	if !g.s.takeUpload("Occupe.wsquashfs") {
		t.Fatal("réservation de test impossible")
	}
	if rec := g.putPart("Occupe.wsquashfs", 0, 4, []byte("hsqs")); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "envoi en cours") {
		t.Errorf("envoi concurrent : %d %s", rec.Code, rec.Body.String())
	}
	g.s.releaseUpload("Occupe.wsquashfs")
}

func TestGamesDelete(t *testing.T) {
	g := newGamesRig(t)
	dir := g.setDir("Games/non-steam")
	name := "Suppr.wsquashfs"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("hsqs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".part"), []byte("hs"), 0o644); err != nil {
		t.Fatal(err)
	}
	del := "/v1/games/file?name=" + name

	// Jeu verrouillé (lancé ou en maintenance) : 409, rien supprimé.
	lock, ok := lockIdleGame("Suppr")
	if !ok {
		t.Fatal("verrou de test impossible")
	}
	if rec := g.req("DELETE", del, nil, nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "jeu monté ou en cours") {
		t.Errorf("jeu verrouillé : %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Errorf("image supprimée malgré le verrou : %v", err)
	}
	lock.Close()

	rec := g.req("DELETE", del, nil, nil)
	if rec.Code != http.StatusOK || decodeGames(t, rec)["removed"] != true {
		t.Fatalf("suppression : %d %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{name, name + ".part"} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s encore présent : %v", p, err)
		}
	}
	if rec := g.req("DELETE", "/v1/games/file?name=Rien.wsquashfs", nil, nil); rec.Code != http.StatusOK || decodeGames(t, rec)["removed"] != false {
		t.Errorf("suppression sans fichier : %d %s", rec.Code, rec.Body.String())
	}
	if rec := g.req("DELETE", "/v1/games/file?name=../x.wsquashfs", nil, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("nom invalide : %d", rec.Code)
	}
}

// TestGamesFirstSendCreatesDir : sans choix manuel ni image détectée, le premier envoi crée
// HOME/Games/non-steam.
func TestGamesFirstSendCreatesDir(t *testing.T) {
	g := newGamesRig(t)
	// L'image de roms/ de la fixture fait de roms/win le dossier détecté : on la retire.
	if err := os.Remove(filepath.Join(g.home, "roms", "win", "Jeu.wsquashfs")); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(g.home, "Games", "non-steam")
	rec := g.req("GET", "/v1/games/list", nil, nil)
	list := decodeGames(t, rec)
	if list["dir"] != want || list["set"] != false || len(list["games"].([]any)) != 0 {
		t.Fatalf("liste sans dossier : %v", list)
	}
	image := []byte("hsqs-premier")
	if rec := g.putPart("Premier.wsquashfs", 0, int64(len(image)), image); rec.Code != http.StatusOK {
		t.Fatalf("premier envoi : %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(want, "Premier.wsquashfs.part")); err != nil {
		t.Errorf("dossier ou .part absent : %v", err)
	}
	if rec := g.commit("Premier.wsquashfs", int64(len(image)), sumHex(image)); rec.Code != http.StatusOK {
		t.Errorf("commit : %d %s", rec.Code, rec.Body.String())
	}
}

func TestGamesInfoVersion(t *testing.T) {
	g := newGamesRig(t)
	rec := g.req("GET", "/v1/info", nil, nil)
	info := decodeGames(t, rec)
	dir, _ := info["games_dir"].(string)
	if info["version"] != agentVersion || !filepath.IsAbs(dir) || info["games_dir_set"] != false {
		t.Errorf("/v1/info : %v", info)
	}
}
