package agent

// Concordance de l'agent avec contract/agent-api.yaml : chaque route de l'agent est appelée avec des
// requêtes valides et refusées, et chaque réponse (code, en-têtes, corps JSON) doit être dans le
// contrat. Les routes /v1/saves/* sont dans cmd/wsquashfs-launcher/contract_test.go (elles ont
// besoin de la synchro du lanceur).

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"wsquashfs-launcher/internal/contracttest"
)

// contractRig : le handler de l'agent et le contrat. Une requête est contrôlée avant l'envoi (sauf
// celles qui visent un refus), la réponse l'est toujours.
type contractRig struct {
	t *testing.T
	c *contracttest.Contract
	h http.Handler
}

// call : requête avec le jeton et l'identifiant de la machine associée par agentFixture ; hdr les
// remplace (valeur "-" : l'en-tête est retiré). valid : la requête elle-même doit être dans le contrat.
func (r *contractRig) call(valid bool, method, target string, hdr map[string]string, body []byte) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("X-Wsq-Machine", "client-machine-01")
	for k, v := range hdr {
		if v == "-" {
			req.Header.Del(k)
			continue
		}
		req.Header.Set(k, v)
	}
	if valid {
		r.c.CheckRequest(r.t, method, target, req.Header, body)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	r.c.CheckResponse(r.t, method, target, rec.Code, rec.Header(), rec.Body.Bytes(), false)
	return rec
}

func (r *contractRig) want(rec *httptest.ResponseRecorder, code int) {
	r.t.Helper()
	if rec.Code != code {
		r.t.Fatalf("code %d, attendu %d : %s", rec.Code, code, rec.Body.String())
	}
}

func jsonBody(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func newContractRig(t *testing.T) (*contractRig, *service) {
	t.Helper()
	svc, h, _ := agentFixture(t)
	return &contractRig{t: t, c: contracttest.Load(t), h: h}, svc
}

// Les routes du mux de http.go sont celles du contrat servies par l'agent, ni plus ni moins.
func TestContractListsEveryAgentRoute(t *testing.T) {
	c := contracttest.Load(t)
	src, err := os.ReadFile("http.go")
	if err != nil {
		t.Fatal(err)
	}
	var code []string
	for _, m := range regexp.MustCompile(`HandleFunc\("([A-Z]+ /v1/[a-z/]+)"`).FindAllStringSubmatch(string(src), -1) {
		code = append(code, m[1])
	}
	var spec []string
	for _, r := range c.Routes() {
		if slices.Contains(r.ServedBy(), "agent") {
			spec = append(spec, r.Method+" "+r.Path)
		}
	}
	slices.Sort(code)
	slices.Sort(spec)
	if !slices.Equal(code, spec) {
		t.Fatalf("routes de http.go : %v\nroutes du contrat servies par l'agent : %v", code, spec)
	}
}

// La version de protocole du contrat est celle de l'agent, et aucune route n'est plus récente.
func TestContractVersionIsAgentVersion(t *testing.T) {
	c := contracttest.Load(t)
	if c.Version() != agentVersion {
		t.Fatalf("info.version du contrat %q, agentVersion %q", c.Version(), agentVersion)
	}
	current, _ := strconv.Atoi(agentVersion)
	for _, r := range c.Routes() {
		if r.Since() < 1 || r.Since() > current {
			t.Errorf("%s %s : x-since-agent %d hors de 1..%d", r.Method, r.Path, r.Since(), current)
		}
	}
}

func TestContractInfo(t *testing.T) {
	r, svc := newContractRig(t)
	rec := r.call(true, "GET", "/v1/info", nil, nil)
	r.want(rec, 200)
	// Avec toute l'activité renseignée et un manager déclaré.
	svc.env.Build = func() string { return "abc123def456" }
	svc.env.Playing = func() []Playing { return []Playing{{Game: "Crazy Taxi 3", Since: 1700000000}} }
	svc.env.Controllers = func() []Controller {
		return []Controller{{Kind: "dualsense", Name: "DualSense", Bus: "bluetooth"}, {Kind: "gamepad", Name: "Pad", Bus: ""}}
	}
	r.want(r.call(true, "GET", "/v1/info", map[string]string{"X-Wsq-Machine": "-"}, nil), 200)
	r.want(r.call(false, "GET", "/v1/info", map[string]string{"Authorization": "Bearer faux"}, nil), 401)
	r.want(r.call(false, "GET", "/v1/info", map[string]string{"X-Wsq-Machine": "autre-machine"}, nil), 401)
	r.want(r.call(false, "GET", "/v1/info", map[string]string{"Authorization": "-"}, nil), 401)
}

func TestContractUnknownRouteIsPlainText(t *testing.T) {
	r, _ := newContractRig(t)
	req := httptest.NewRequest("GET", "/v1/shell", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	r.want(rec, 404)
	r.c.CheckResponse(t, "GET", "/v1/info", rec.Code, rec.Header(), rec.Body.Bytes(), false)
	if strings.Contains(rec.Body.String(), "detail") {
		t.Errorf("une route inconnue répond sans detail : %s", rec.Body.String())
	}
}

func TestContractDoctor(t *testing.T) {
	r, _ := newContractRig(t)
	fakeLauncher(t, `cat <<'EOF'
{"code":0,"sections":[{"title":"Système","items":[{"status":"ok","text":"x"},{"status":"missing","text":"y","details":["z"]}]}],"runners":[{"name":"GE-Proton","path":"/opt/ge"}],"verdict":"✓ Prêt à lancer des jeux"}
EOF`)
	r.want(r.call(true, "GET", "/v1/doctor?refresh=1", nil, nil), 200)
	fakeLauncher(t, `exit 1`)
	r.want(r.call(true, "GET", "/v1/doctor?refresh=1", nil, nil), 503)
}

func TestContractTrial(t *testing.T) {
	r, _ := newContractRig(t)
	fakeLauncher(t, `echo '{"code":0,"elapsed":10,"launcher_alive":true,"game_seen":true,"games":["Jeu"],"windows":[{"id":"0x1","name":"Jeu","state":""}],"left":{"procs":0,"mounts":0},"screenshot":false,"log":""}'`)
	rec := r.call(true, "POST", "/v1/trial", nil, jsonBody(map[string]any{"rel": "win/Jeu.wsquashfs", "duration": 10}))
	r.want(rec, 200)
	// Sans durée : 60 s par défaut.
	fakeLauncher(t, `echo '{"error":"busy","code":0,"elapsed":0,"launcher_alive":false,"game_seen":false,"games":null,"windows":null,"left":{"procs":0,"mounts":0},"screenshot":false,"log":""}'; exit 3`)
	r.want(r.call(true, "POST", "/v1/trial", nil, jsonBody(map[string]any{"rel": "win/Jeu.wsquashfs"})), 409)
	fakeLauncher(t, `exit 0`) // pas de rapport
	r.want(r.call(true, "POST", "/v1/trial", nil, jsonBody(map[string]any{"rel": "win/Jeu.wsquashfs", "duration": 10})), 500)
	r.want(r.call(false, "POST", "/v1/trial", nil, jsonBody(map[string]any{"rel": "win/Jeu.wsquashfs", "duration": 5})), 400)
	r.want(r.call(false, "POST", "/v1/trial", nil, []byte("pas du json")), 400)
	r.want(r.call(true, "POST", "/v1/trial", nil, jsonBody(map[string]any{"rel": "win/Absent.wsquashfs", "duration": 10})), 404)
	r.want(r.call(false, "POST", "/v1/trial", nil, jsonBody(map[string]any{"rel": "../x.wsquashfs", "duration": 10})), 400)
}

func TestContractMaintenance(t *testing.T) {
	r, _ := newContractRig(t)
	fakeLauncher(t, `echo "sortie $*"`)
	r.want(r.call(true, "POST", "/v1/maintenance", nil, jsonBody(map[string]any{"action": "list"})), 200)
	r.want(r.call(true, "POST", "/v1/maintenance", nil, jsonBody(map[string]any{"action": "save", "rel": "win/Jeu.wsquashfs"})), 200)
	fakeLauncher(t, `echo oups; exit 7`)
	rec := r.call(true, "POST", "/v1/maintenance", nil, jsonBody(map[string]any{"action": "clean"}))
	r.want(rec, 200)
	if !strings.Contains(rec.Body.String(), `"code":7`) {
		t.Errorf("code de sortie du lanceur attendu : %s", rec.Body.String())
	}
	r.want(r.call(false, "POST", "/v1/maintenance", nil, jsonBody(map[string]any{"action": "erase"})), 400)
	r.want(r.call(false, "POST", "/v1/maintenance", nil, jsonBody(map[string]any{"action": "list", "rel": "win/Jeu.wsquashfs"})), 400)
	r.want(r.call(false, "POST", "/v1/maintenance", nil, jsonBody(map[string]any{"action": "save"})), 400)
	r.want(r.call(true, "POST", "/v1/maintenance", nil, jsonBody(map[string]any{"action": "sync", "rel": "win/Absent.wsquashfs"})), 404)
}

func TestContractUpdate(t *testing.T) {
	r, svc := newContractRig(t)
	// Sans Build ni Restart : mise à jour indisponible.
	r.want(r.call(true, "POST", "/v1/update", nil, nil), 501)
	oldDelay := restartDelay
	t.Cleanup(func() { restartDelay = oldDelay })
	restartDelay = 0
	fakeLauncher(t, `echo "Lanceur mis à jour"`)
	builds := []string{"ancienne", "nouvelle"}
	calls := 0
	svc.env.Build = func() string { calls++; return builds[min(calls-1, 1)] }
	svc.env.Restart = func() error { return nil }
	r.want(r.call(true, "POST", "/v1/update", nil, nil), 200)
	time.Sleep(50 * time.Millisecond)
	svc.gamesMu.Lock()
	svc.uploads = map[string]bool{"Jeu": true}
	svc.gamesMu.Unlock()
	r.want(r.call(true, "POST", "/v1/update", nil, nil), 409)
}

func TestContractRunner(t *testing.T) {
	r, _ := newContractRig(t)
	home := os.Getenv("HOME")
	runner := filepath.Join(home, ".local/share/wsquashfs/wine/wine-test")
	if err := os.MkdirAll(runner+"/bin", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runner+"/bin/wine", []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("bin/wine", runner+"/wine-lien"); err != nil {
		t.Fatal(err)
	}
	r.want(r.call(true, "GET", "/v1/runner/list?path="+runner, nil, nil), 200)
	rec := r.call(true, "GET", "/v1/runner/tar?path="+runner, nil, nil)
	r.want(rec, 200)
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-tar" {
		t.Errorf("type de contenu %q", ct)
	}
	r.want(r.call(false, "GET", "/v1/runner/list?path=relatif", nil, nil), 400)
	r.want(r.call(false, "GET", "/v1/runner/list?path=/etc", nil, nil), 403)
	r.want(r.call(true, "GET", "/v1/runner/list?path="+runner+"/absent", nil, nil), 404)
	other := filepath.Join(home, "hors-runner")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	r.want(r.call(true, "GET", "/v1/runner/list?path="+other, nil, nil), 403)
	r.want(r.call(true, "GET", "/v1/runner/tar?path="+other, nil, nil), 403)
}

func TestContractGames(t *testing.T) {
	r, _ := newContractRig(t)
	home, err := filepath.EvalSymlinks(os.Getenv("HOME"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "Games", "contrat")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.want(r.call(true, "POST", "/v1/games/dir", nil, jsonBody(map[string]string{"dir": dir})), 200)
	r.want(r.call(false, "POST", "/v1/games/dir", nil, jsonBody(map[string]string{"dir": "/usr"})), 400)
	r.want(r.call(true, "GET", "/v1/games/list", nil, nil), 200)

	image := []byte("0123456789abcdef")
	total := strconv.Itoa(len(image))
	put := func(valid bool, name string, offset int, body []byte, totalSize string) *httptest.ResponseRecorder {
		return r.call(valid, "PUT", "/v1/games/file?name="+name+"&offset="+strconv.Itoa(offset),
			map[string]string{"X-Wsq-Total-Size": totalSize}, body)
	}
	r.want(r.call(true, "GET", "/v1/games/part?name=Jeu.wsquashfs", nil, nil), 200)
	r.want(put(true, "Jeu.wsquashfs", 0, image[:8], total), 200)
	r.want(r.call(true, "GET", "/v1/games/part?name=Jeu.wsquashfs", nil, nil), 200)
	rec := put(true, "Jeu.wsquashfs", 0, image[:8], total) // mauvais décalage : 409 avec part_size
	r.want(rec, 409)
	if !strings.Contains(rec.Body.String(), `"part_size":8`) {
		t.Errorf("part_size attendu : %s", rec.Body.String())
	}
	r.want(r.call(true, "GET", "/v1/games/list", nil, nil), 200)
	r.want(put(false, "Jeu.wsquashfs", 8, image[8:], "0"), 400)
	r.want(put(false, "dossier/Jeu.wsquashfs", 0, image, total), 400)
	r.want(put(true, "Jeu.wsquashfs", 8, image[8:], total), 200)
	sum := sumHex(image)
	r.want(r.call(true, "POST", "/v1/games/commit", nil, jsonBody(map[string]any{"name": "Jeu.wsquashfs", "size": 99, "sha256": sum})), 409)
	r.want(r.call(true, "POST", "/v1/games/commit", nil, jsonBody(map[string]any{"name": "Jeu.wsquashfs", "size": len(image), "sha256": strings.Repeat("0", 64)})), 422)
	r.want(r.call(true, "POST", "/v1/games/commit", nil, jsonBody(map[string]any{"name": "Jeu.wsquashfs", "size": len(image), "sha256": sum})), 404) // .part supprimé par l'échec
	r.want(put(true, "Jeu.wsquashfs", 0, image, total), 200)
	r.want(r.call(true, "POST", "/v1/games/commit", nil, jsonBody(map[string]any{"name": "Jeu.wsquashfs", "size": len(image), "sha256": sum})), 200)
	r.want(r.call(false, "POST", "/v1/games/commit", nil, jsonBody(map[string]any{"name": "Jeu.wsquashfs", "size": len(image), "sha256": "court"})), 400)
	r.want(put(true, "Jeu.wsquashfs", 0, image, total), 409) // déjà installé
	r.want(r.call(true, "GET", "/v1/games/part?name=Jeu.wsquashfs", nil, nil), 200)
	r.want(r.call(true, "GET", "/v1/games/list", nil, nil), 200)
	testLockedGame = "Jeu"
	r.want(r.call(true, "DELETE", "/v1/games/file?name=Jeu.wsquashfs", nil, nil), 409)
	testLockedGame = ""
	r.want(r.call(true, "DELETE", "/v1/games/file?name=Jeu.wsquashfs", nil, nil), 200)
	r.want(r.call(true, "DELETE", "/v1/games/file?name=Absent.wsquashfs", nil, nil), 200)
	r.want(r.call(false, "DELETE", "/v1/games/file?name=x", nil, nil), 400)
	// Retour à la détection.
	r.want(r.call(true, "POST", "/v1/games/dir", nil, jsonBody(map[string]string{"dir": ""})), 200)
}

func TestContractPair(t *testing.T) {
	r, _ := newContractRig(t)
	pair := func(valid bool, secret, machine string, body []byte) *httptest.ResponseRecorder {
		hdr := map[string]string{"Authorization": "Bearer " + secret, "X-Wsq-Machine": machine}
		if machine == "" {
			hdr["X-Wsq-Machine"] = "-"
		}
		return r.call(valid, "POST", "/v1/pair", hdr, body)
	}
	const secret = "secret-contrat-01"
	r.want(pair(false, secret, "aaaa-machine-01", jsonBody(map[string]string{"name": "x"})), 401) // aucun code ouvert
	if err := beginPairing(secret, time.Minute); err != nil {
		t.Fatal(err)
	}
	r.want(pair(false, secret, "", jsonBody(map[string]string{"name": "x"})), 400)
	r.want(pair(false, secret, "aaaa-machine-01", []byte("pas du json")), 400)
	r.want(pair(false, secret, "aaaa-machine-01", []byte(`{"name":"`+strings.Repeat("x", 5000)+`"}`)), 413)
	r.want(pair(false, "mauvais", "aaaa-machine-01", jsonBody(map[string]string{"name": "x"})), 401)
	rec := pair(true, secret, "aaaa-machine-01", jsonBody(map[string]string{"name": "SteamBox"}))
	r.want(rec, 200)
	// Le jeton rendu ouvre l'agent.
	var v struct{ Token string }
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	r.want(r.call(true, "GET", "/v1/info", map[string]string{"Authorization": "Bearer " + v.Token, "X-Wsq-Machine": "aaaa-machine-01"}, nil), 200)
	r.want(pair(false, secret, "aaaa-machine-01", jsonBody(map[string]string{"name": "x"})), 401) // code consommé
}
