package main

// Tests de l'agent : authentification, chemins autorisés, certificat et code
// d'association. Pas de réseau réel : les routes sont appelées en mémoire.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// agentFixture : configuration isolée, une racine roms/ avec une image, et un
// faux lanceur (pour les routes qui l'appellent).
func agentFixture(t *testing.T) (*agentServer, http.Handler, string) {
	t.Helper()
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
	selfPath = fake
	cfg := agentConfig{Port: 8790, Token: "secret-token", Roms: []string{roms}, Session: "emulationstation"}
	if err := saveAgentConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := ensureAgentCert(); err != nil {
		t.Fatal(err)
	}
	s := newAgentServer(cfg, false)
	return s, s.httpServer().Handler, roms
}

func do(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAgentRequiresToken(t *testing.T) {
	_, h, _ := agentFixture(t)
	if rec := do(h, "GET", "/v1/info", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("sans jeton : code %d, attendu 401", rec.Code)
	}
	if rec := do(h, "GET", "/v1/info", "mauvais", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("mauvais jeton : code %d, attendu 401", rec.Code)
	}
	if rec := do(h, "GET", "/v1/info", "secret-token", ""); rec.Code != http.StatusOK {
		t.Errorf("bon jeton : code %d, attendu 200", rec.Code)
	}
}

func TestAgentDoctorPassesLauncherJSON(t *testing.T) {
	_, h, _ := agentFixture(t)
	rec := do(h, "GET", "/v1/doctor", "secret-token", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"verdict":"ok"`) {
		t.Errorf("doctor : code %d, corps %q", rec.Code, rec.Body.String())
	}
}

func TestResolveImageRefusesEscapes(t *testing.T) {
	s, _, roms := agentFixture(t)
	outside := filepath.Join(filepath.Dir(roms), "outside.wsquashfs")
	if err := os.WriteFile(outside, []byte("hsqs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(roms, "win", "lien.wsquashfs")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]int{
		"/etc/passwd.wsquashfs": http.StatusBadRequest,
		"../outside.wsquashfs":  http.StatusBadRequest,
		"win/../../x.wsquashfs": http.StatusBadRequest,
		"win/Jeu.txt":           http.StatusBadRequest,
		"win/lien.wsquashfs":    http.StatusNotFound, // lien qui sort de roms/
		"win/absent.wsquashfs":  http.StatusNotFound,
		"win/Jeu.wsquashfs":     0,
	}
	for rel, want := range cases {
		_, code, _ := s.resolveImage(rel)
		if code != want {
			t.Errorf("resolveImage(%q) = %d, attendu %d", rel, code, want)
		}
	}
}

// TestMaintenanceOnlyListedActions : seules les actions de la table sont acceptées, et la règle sur
// le jeu (rel) est respectée : interdit pour list, obligatoire pour save.
func TestMaintenanceOnlyListedActions(t *testing.T) {
	_, h, _ := agentFixture(t)
	for _, body := range []string{
		`{"action":"clear"}`, `{"action":"erase"}`, `{"action":"pack"}`, `{"action":"install"}`,
		`{"action":"trial"}`, `{"action":"agent"}`, `{"action":"update"}`, `{"action":"saves-all"}`,
		`{"action":"list","rel":"win/Jeu.wsquashfs"}`,
		`{"action":"umount","rel":"win/Jeu.wsquashfs"}`,
		`{"action":"save"}`,
		`{"action":"saves","rel":"win/Jeu.wsquashfs"}`,
		`{"action":"sync-status","rel":"win/Jeu.wsquashfs"}`,
		`{"action":"save","rel":"../Jeu.wsquashfs"}`,
	} {
		if rec := do(h, "POST", "/v1/maintenance", "secret-token", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s accepté (code %d) : doit être refusé", body, rec.Code)
		}
	}
	if rec := do(h, "POST", "/v1/maintenance", "secret-token", `{"action":"list"}`); rec.Code != http.StatusOK {
		t.Errorf("--list refusé (code %d)", rec.Code)
	}
}

// TestMaintenanceArgs : arguments réellement passés au lanceur pour chaque action de la table. Le
// faux lanceur affiche chaque argument entre chevrons.
func TestMaintenanceArgs(t *testing.T) {
	_, h, roms := agentFixture(t)
	fake := filepath.Join(t.TempDir(), "fake-launcher")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '<%s>' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	selfPath = fake
	image, err := filepath.EvalSymlinks(filepath.Join(roms, "win", "Jeu.wsquashfs"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ body, want string }{
		{`{"action":"save","rel":"win/Jeu.wsquashfs"}`, "<--save><" + image + ">"},
		{`{"action":"saves"}`, "<--saves>"},
		{`{"action":"sync"}`, "<--sync>"},
		{`{"action":"sync","rel":"win/Jeu.wsquashfs"}`, "<--sync><" + image + ">"},
		{`{"action":"sync-status"}`, "<--sync><status>"},
		{`{"action":"list"}`, "<--list>"},
		{`{"action":"umount"}`, "<--umount>"},
		{`{"action":"clean"}`, "<--clean>"},
	}
	for _, c := range cases {
		rec := do(h, "POST", "/v1/maintenance", "secret-token", c.body)
		var got struct {
			Output string `json:"output"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Errorf("%s : code %d, réponse %s", c.body, rec.Code, rec.Body.String())
			continue
		}
		if got.Output != c.want {
			t.Errorf("%s : arguments %q, attendus %q", c.body, got.Output, c.want)
		}
	}
	// save sans jeu : 400 avec le motif.
	rec := do(h, "POST", "/v1/maintenance", "secret-token", `{"action":"save"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "--save attend un jeu") {
		t.Errorf("save sans jeu : code %d, %s", rec.Code, rec.Body.String())
	}
	// Version de l'API : 4 depuis la table (save, saves, sync, sync-status, taille et OS).
	rec = do(h, "GET", "/v1/info", "secret-token", "")
	var info struct {
		Version string `json:"version"`
		OS      string `json:"os"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil || info.Version != "5" || info.OS == "" {
		t.Errorf("/v1/info : version et os attendus, %s (%v)", rec.Body.String(), err)
	}
}

func TestTrialValidatesDuration(t *testing.T) {
	_, h, _ := agentFixture(t)
	for _, body := range []string{`{"rel":"win/Jeu.wsquashfs","duration":5}`, `{"rel":"win/Jeu.wsquashfs","duration":601}`} {
		if rec := do(h, "POST", "/v1/trial", "secret-token", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s : code %d, attendu 400", body, rec.Code)
		}
	}
}

func TestRunnerRoutesOnlyInstalledRunners(t *testing.T) {
	_, h, _ := agentFixture(t)
	if rec := do(h, "GET", "/v1/runner/list?path=/etc", "secret-token", ""); rec.Code != http.StatusForbidden {
		t.Errorf("/etc lu comme runner (code %d)", rec.Code)
	}
	if rec := do(h, "GET", "/v1/runner/list?path=relatif", "secret-token", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("chemin relatif accepté (code %d)", rec.Code)
	}
}

func TestPairCodeMatchesCertificate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := agentConfig{Port: 8790, Token: "jeton_TEST-123"}
	if err := ensureAgentCert(); err != nil {
		t.Fatal(err)
	}
	fp, err := certFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	// L'empreinte est celle du DER du certificat (SHA-256, hexadécimal).
	data, _ := os.ReadFile(filepath.Join(agentDir(), "cert.pem"))
	block, _ := pem.Decode(data)
	sum := sha256.Sum256(block.Bytes)
	if want := strings.ToUpper(hex.EncodeToString(sum[:])); fp != want {
		t.Errorf("empreinte %s, attendu %s", fp, want)
	}
	code := pairCode(cfg, "192.168.1.20", fp)
	re := regexp.MustCompile(`^wsq://([A-Za-z0-9_-]+)@([^:]+):(\d+)#([0-9A-F]{64})$`)
	m := re.FindStringSubmatch(code)
	if m == nil {
		t.Fatalf("code d'association mal formé : %q", code)
	}
	if m[1] != "jeton_TEST-123" || m[2] != "192.168.1.20" || m[3] != "8790" || m[4] != fp {
		t.Errorf("code d'association incorrect : %q", code)
	}
}

func TestCertificateIsEcdsaTenYears(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := ensureAgentCert(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(agentDir(), "cert.pem"))
	if !strings.Contains(string(data), "BEGIN CERTIFICATE") {
		t.Fatal("certificat PEM absent")
	}
	keyInfo, err := os.Stat(filepath.Join(agentDir(), "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Errorf("clé en %v, attendu 600", keyInfo.Mode().Perm())
	}
}

func TestNewTokenIsRandom(t *testing.T) {
	a, _ := newToken()
	b, _ := newToken()
	if a == b || len(a) < 40 {
		t.Errorf("jetons identiques ou trop courts : %q %q", a, b)
	}
}

// TestMaintenanceForcesColor : la sortie de maintenance est colorée pour le
// manager (CLICOLOR_FORCE=1 transmis au lanceur appelé).
func TestMaintenanceForcesColor(t *testing.T) {
	_, h, _ := agentFixture(t)
	fake := filepath.Join(t.TempDir(), "fake-launcher")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"force=$CLICOLOR_FORCE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	selfPath = fake
	rec := do(h, "POST", "/v1/maintenance", "secret-token", `{"action":"list"}`)
	if !strings.Contains(rec.Body.String(), "force=1") {
		t.Errorf("couleurs non forcées pour la maintenance : %s", rec.Body.String())
	}
}

// TestAgentStop : --agent stop arrête le processus inscrit dans le fichier de
// PID s'il est un agent, et ignore un PID périmé (sans tuer un autre processus).
func TestAgentStop(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	if err := os.MkdirAll(agentDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// Faux agent : sa ligne de commande contient « --agent » (argument $0 de
	// sh). Piège : avec « sh -c "sleep 30" », dash exécute directement sleep
	// (exec du dernier ordre), « --agent » disparaît de /proc/<pid>/cmdline et
	// cmdAgentStop croit le PID périmé. « ; : » interdit ce remplacement : sh
	// reste le processus inscrit et meurt sur SIGTERM. Son enfant « sleep »
	// survit alors à sh : il est dans un groupe de processus propre, tué
	// entièrement au nettoyage.
	fake := exec.Command("sh", "-c", "sleep 30; :", "--agent")
	fake.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := fake.Start(); err != nil {
		t.Fatal(err)
	}
	// Course : Start rend la main dès l'execve réussi, mais le noyau n'a pas
	// encore renseigné les arguments du nouveau processus : /proc/<pid>/cmdline
	// est vide un bref instant. On attend qu'il contienne « --agent ».
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", fake.Process.Pid)); strings.Contains(string(data), "--agent\x00") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("faux agent : « --agent » absent de sa ligne de commande")
		}
	}
	// Récupéré dès sa fin (comme le ferait le système pour un agent lancé
	// hors du shell) : sinon il reste zombie et « kill 0 » le voit encore.
	reaped := make(chan struct{})
	go func() { fake.Wait(); close(reaped) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-fake.Process.Pid, syscall.SIGKILL) // groupe : sh et sleep
		<-reaped
	})
	if err := os.WriteFile(agentPidFile(), []byte(strconv.Itoa(fake.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := capteUI(t)
	if code := cmdAgentStop(); code != 0 {
		t.Fatalf("code %d", code)
	}
	<-reaped
	if !strings.Contains(out.String(), "Agent arrêté") {
		t.Errorf("sortie : %q", out.String())
	}
	if pathExistsFile(agentPidFile()) {
		t.Error("fichier de PID gardé après l'arrêt")
	}

	// PID périmé : un processus quelconque (ici le test lui-même) n'est pas touché.
	if err := os.WriteFile(agentPidFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdAgentStop(); code != 0 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(out.String(), "périmé") || pathExistsFile(agentPidFile()) {
		t.Errorf("PID périmé mal traité : %q", out.String())
	}
}

func pathExistsFile(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// TestMaintenanceReturnsExitCode : la réponse porte le code de sortie du lanceur (0 ou 3), comme le
// mode local ; wsquashfs-manager s'en sert pour signaler un échec.
func TestMaintenanceReturnsExitCode(t *testing.T) {
	_, h, _ := agentFixture(t)
	fake := filepath.Join(t.TempDir(), "fake-launcher")
	for _, c := range []struct {
		script string
		code   int
	}{{"echo sortie\nexit 3", 3}, {"echo sortie\nexit 0", 0}} {
		if err := os.WriteFile(fake, []byte("#!/bin/sh\n"+c.script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		selfPath = fake
		rec := do(h, "POST", "/v1/maintenance", "secret-token", `{"action":"list"}`)
		var got struct {
			Code   *int   `json:"code"`
			Output string `json:"output"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Fatalf("exit %d : code HTTP %d, %s", c.code, rec.Code, rec.Body.String())
		}
		if got.Code == nil || *got.Code != c.code {
			t.Errorf("exit %d : code %v, attendu %d", c.code, got.Code, c.code)
		}
		if !strings.Contains(got.Output, "sortie") {
			t.Errorf("sortie absente : %q", got.Output)
		}
	}
}

// TestOSID : identifiant de distribution tiré d'os-release ; "linux" sans fichier ni ID.
func TestOSID(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := []struct {
		name, path, want string
	}{
		{"steamos", write("steamos", "NAME=\"SteamOS\"\nID=steamos\nVERSION_ID=3.5\n"), "steamos"},
		{"batocera, guillemets et majuscules", write("batocera", "ID_LIKE=linux\nID=\"Batocera\"\n"), "batocera"},
		{"absent", filepath.Join(dir, "absent"), "linux"},
		{"sans ID", write("sans", "NAME=Inconnu\nID_LIKE=debian\n"), "linux"},
		{"ID vide", write("vide", "ID=\"\"\n"), "linux"},
	}
	for _, c := range cases {
		if got := osID(c.path); got != c.want {
			t.Errorf("%s : osID = %q, attendu %q", c.name, got, c.want)
		}
	}
}
