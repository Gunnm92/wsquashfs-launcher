package savesync

import (
	"os"
	"strings"
	"testing"
)

func TestParsePairCode(t *testing.T) {
	fp := strings.Repeat("ab", 32)
	p, err := ParsePairCode("wsq://tok_EN-1@192.168.1.20:8790#" + fp)
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "https://192.168.1.20:8790" || p.Token != "tok_EN-1" || p.Fingerprint != strings.ToUpper(fp) || p.Name != "" {
		t.Errorf("code valide : %+v", p)
	}
	p, err = ParsePairCode("wsq://tok@[fd00::20]:8790#" + fp)
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
		if _, err := ParsePairCode(code); err == nil {
			t.Errorf("code accepté : %q", code)
		}
	}
}

func TestSyncConfigRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := LoadConfig()
	if !ValidID(cfg.ID) {
		t.Fatalf("identifiant généré invalide : %q", cfg.ID)
	}
	if _, err := os.Stat(syncConfigPath()); err == nil {
		t.Fatal("LoadConfig a écrit le fichier")
	}
	cfg.Peers = []Peer{{Name: "aibox", URL: "https://10.0.0.2:8790", Token: "t", Fingerprint: "F"}}
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(syncConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("droits %v, attendu 600", st.Mode().Perm())
	}
	again := LoadConfig()
	if again.ID != cfg.ID || len(again.Peers) != 1 || again.Peers[0].Name != "aibox" {
		t.Errorf("relecture : %+v", again)
	}
}
