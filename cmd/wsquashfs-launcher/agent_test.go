package main

// Tests de l'agent : authentification, chemins autorisés, certificat et code
// d'association. Pas de réseau réel : les routes sont appelées en mémoire.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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

func TestMaintenanceOnlyListedActions(t *testing.T) {
	_, h, _ := agentFixture(t)
	if rec := do(h, "POST", "/v1/maintenance", "secret-token", `{"action":"clear"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("--clear accepté (code %d) : doit être refusé", rec.Code)
	}
	if rec := do(h, "POST", "/v1/maintenance", "secret-token", `{"action":"list","rel":"win/Jeu.wsquashfs"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("--list avec un jeu accepté (code %d)", rec.Code)
	}
	if rec := do(h, "POST", "/v1/maintenance", "secret-token", `{"action":"list"}`); rec.Code != http.StatusOK {
		t.Errorf("--list refusé (code %d)", rec.Code)
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
