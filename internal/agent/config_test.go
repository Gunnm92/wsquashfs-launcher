package agent

// Tests des réglages du manager (config.go) : GET /v1/config et PUT /v1/config/{defaults,optiscaler}.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// configFileReply : un fichier de réglages tel que GET /v1/config le rend.
type configFileReply struct {
	Path string  `json:"path"`
	Text string  `json:"text"`
	Sha  *string `json:"sha"`
}

type configGetReply struct {
	GPU struct {
		NVIDIA bool `json:"nvidia"`
		AMD    bool `json:"amd"`
	} `json:"gpu"`
	Defaults   configFileReply `json:"defaults"`
	Optiscaler struct {
		Installed bool            `json:"installed"`
		Version   string          `json:"version"`
		Template  string          `json:"template"`
		Overrides configFileReply `json:"overrides"`
	} `json:"optiscaler"`
	Env map[string]string `json:"env"`
}

func shaOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func getConfig(t *testing.T, h http.Handler) configGetReply {
	t.Helper()
	rec := do(h, "GET", "/v1/config", "secret-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/config : code %d, %s", rec.Code, rec.Body.String())
	}
	var v configGetReply
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	return v
}

func TestConfigGetVide(t *testing.T) {
	_, h, _ := agentFixture(t)
	drm := t.TempDir()
	_ = os.MkdirAll(filepath.Join(drm, "card0", "device"), 0o755)
	_ = os.WriteFile(filepath.Join(drm, "card0", "device", "vendor"), []byte("0x10de\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(drm, "card1", "device"), 0o755)
	_ = os.WriteFile(filepath.Join(drm, "card1", "device", "vendor"), []byte("0x1002\n"), 0o644)
	t.Setenv("WSQUASHFS_DRM_SYSFS", drm)
	t.Setenv("WSQUASHFS_FSR", "1")
	t.Setenv("WSQUASHFS_API_TOKEN", "ne-doit-pas-sortir")
	t.Setenv("WSQUASHFS_DB_PASSWORD", "ne-doit-pas-sortir")
	t.Setenv("WSQUASHFS_SIGNING_KEY", "ne-doit-pas-sortir")
	t.Setenv("WSQUASHFS_CLIENT_SECRET", "ne-doit-pas-sortir")

	v := getConfig(t, h)
	if !v.GPU.NVIDIA || !v.GPU.AMD {
		t.Errorf("cartes : %+v", v.GPU)
	}
	if v.Defaults.Sha != nil || v.Defaults.Text != "" || v.Defaults.Path != filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wsquashfs", "defaults.autorun") {
		t.Errorf("défauts absents : %+v", v.Defaults)
	}
	if v.Optiscaler.Installed || v.Optiscaler.Version != "" || v.Optiscaler.Template != "" || v.Optiscaler.Overrides.Sha != nil {
		t.Errorf("OptiScaler non installé : %+v", v.Optiscaler)
	}
	if v.Env["WSQUASHFS_FSR"] != "1" {
		t.Errorf("env : %v", v.Env)
	}
	for k := range v.Env {
		if secretNom(k) {
			t.Errorf("variable sensible exposée : %s", k)
		}
	}
}

func TestConfigGetOptiscalerInstalle(t *testing.T) {
	_, h, _ := agentFixture(t)
	base := optiscalerBaseDir()
	_ = os.MkdirAll(base, 0o755)
	_ = os.WriteFile(filepath.Join(base, "OptiScaler.dll"), []byte("dll"), 0o644)
	_ = os.WriteFile(filepath.Join(base, "VERSION"), []byte("0.9.1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(base, "OptiScaler.ini"), []byte("[Dxgi]\nFps=60\n"), 0o644)
	v := getConfig(t, h)
	if !v.Optiscaler.Installed || v.Optiscaler.Version != "0.9.1" || v.Optiscaler.Template != "[Dxgi]\nFps=60\n" {
		t.Errorf("OptiScaler installé : %+v", v.Optiscaler)
	}
}

func TestConfigDefaultsPutEcritEtSupprime(t *testing.T) {
	_, h, _ := agentFixture(t)
	chemin := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wsquashfs", "defaults.autorun")

	texte := "WSQUASHFS_FSR=1\n# note\n"
	rec := do(h, "PUT", "/v1/config/defaults", "secret-token", `{"text":`+jsonString(texte)+`,"base_sha":null}`)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"sha":"`+shaOf(texte)+`"}`+"\n" {
		t.Fatalf("écriture : %d %s", rec.Code, rec.Body.String())
	}
	if data, _ := os.ReadFile(chemin); string(data) != texte {
		t.Fatalf("fichier : %q", data)
	}
	if st, _ := os.Stat(chemin); st.Mode().Perm() != 0o644 {
		t.Errorf("mode %v, attendu 0644", st.Mode().Perm())
	}
	if v := getConfig(t, h); v.Defaults.Sha == nil || *v.Defaults.Sha != shaOf(texte) {
		t.Errorf("GET après écriture : %+v", v.Defaults)
	}

	// Empreinte périmée : 409, rien n'est écrit.
	rec = do(h, "PUT", "/v1/config/defaults", "secret-token", `{"text":"FSR=0\n","base_sha":null}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("base_sha absent alors que le fichier existe : code %d", rec.Code)
	}
	// Bonne empreinte : écrit.
	nouveau := "FSR=0\n"
	rec = do(h, "PUT", "/v1/config/defaults", "secret-token", `{"text":`+jsonString(nouveau)+`,"base_sha":"`+shaOf(texte)+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("remplacement : %d %s", rec.Code, rec.Body.String())
	}

	// Texte vide : fichier supprimé, sha à null.
	rec = do(h, "PUT", "/v1/config/defaults", "secret-token", `{"text":" \n ","base_sha":"`+shaOf(nouveau)+`"}`)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"sha\":null}\n" {
		t.Fatalf("suppression : %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(chemin); !os.IsNotExist(err) {
		t.Errorf("fichier encore présent après suppression : %v", err)
	}
	// Déjà absent : suppression sans erreur si la base concorde.
	rec = do(h, "PUT", "/v1/config/defaults", "secret-token", `{"text":"","base_sha":null}`)
	if rec.Code != http.StatusOK {
		t.Errorf("suppression d'un fichier absent : %d %s", rec.Code, rec.Body.String())
	}
}

func TestConfigOptiscalerPutEcrit(t *testing.T) {
	_, h, _ := agentFixture(t)
	chemin := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wsquashfs", "optiscaler.ini")
	texte := "[Dxgi]\nFps = 60\n; commentaire\nNouvelle.cle=1\n"
	rec := do(h, "PUT", "/v1/config/optiscaler", "secret-token", `{"text":`+jsonString(texte)+`,"base_sha":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("écriture : %d %s", rec.Code, rec.Body.String())
	}
	if data, _ := os.ReadFile(chemin); string(data) != texte {
		t.Errorf("fichier : %q", data)
	}
	if v := getConfig(t, h); v.Optiscaler.Overrides.Sha == nil || *v.Optiscaler.Overrides.Sha != shaOf(texte) {
		t.Errorf("GET : %+v", v.Optiscaler.Overrides)
	}
}

func TestConfigPutRefuse(t *testing.T) {
	_, h, _ := agentFixture(t)
	chemin := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wsquashfs", "defaults.autorun")
	cas := []struct {
		nom, route, corps, detail string
		code                      int
	}{
		{"ligne de forme inconnue", "defaults", `{"text":"CLE SANS EGAL\n","base_sha":null}`, "ligne 1", 422},
		{"numéro de ligne", "defaults", `{"text":"# ok\nFSR=1\n\n1BAD=2\n","base_sha":null}`, "ligne 4", 422},
		{"octet NUL", "defaults", `{"text":"FSR=1\u0000\n","base_sha":null}`, "NUL", 422},
		{"plus de 64 Kio", "defaults", `{"text":` + jsonString(strings.Repeat("A=1\n", 16400)) + `,"base_sha":null}`, "64 Kio", 422},
		{"section OptiScaler invalide", "optiscaler", `{"text":"[Dxgi]\nFps = 60\nbizarre\n","base_sha":null}`, "ligne 3", 422},
		{"JSON invalide", "defaults", `pas du json`, "requête illisible", 400},
		{"text absent", "defaults", `{"base_sha":null}`, "requête illisible", 400},
		{"corps trop grand", "defaults", `{"text":` + jsonString(strings.Repeat("x", 600<<10)) + `,"base_sha":null}`, "", 413},
	}
	for _, c := range cas {
		rec := do(h, "PUT", "/v1/config/"+c.route, "secret-token", c.corps)
		if rec.Code != c.code {
			t.Errorf("%s : code %d, attendu %d : %s", c.nom, rec.Code, c.code, rec.Body.String())
			continue
		}
		if c.detail != "" && !strings.Contains(detailOf(t, rec), c.detail) {
			t.Errorf("%s : détail %q, attendu %q", c.nom, detailOf(t, rec), c.detail)
		}
	}
	if _, err := os.Stat(chemin); !os.IsNotExist(err) {
		t.Errorf("une écriture refusée a créé le fichier")
	}
}

// jsonString : une chaîne JSON (guillemets et échappements compris).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
