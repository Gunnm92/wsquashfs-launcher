package savesync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNotifyManagerPostsEvent : l'événement part vers le manager déclaré (jeton, identifiant de la
// machine, certificat épinglé) ; sans manager déclaré, rien n'est envoyé.
func TestNotifyManagerPostsEvent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got := make(chan map[string]string, 4)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- map[string]string{"path": r.Method + " " + r.URL.Path, "auth": r.Header.Get("Authorization"),
			"machine": r.Header.Get("X-Wsq-Machine"), "body": string(body)}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	s := New(Env{})

	// Sans manager déclaré : rien.
	s.NotifyManager(map[string]string{"event": "start"})
	select {
	case r := <-got:
		t.Fatalf("envoi sans manager : %v", r)
	default:
	}

	cfg := LoadConfig()
	cfg.Peers = []Peer{{Name: "manager", URL: srv.URL, Token: "jeton", Fingerprint: strings.ToUpper(hex.EncodeToString(sum[:])), Manager: true}}
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	s.NotifyManager(map[string]any{"event": "exit", "game": "Jeu", "code": 0})
	r := <-got
	if r["path"] != "POST /v1/events" || r["auth"] != "Bearer jeton" || r["machine"] != cfg.ID {
		t.Fatalf("requête : %v", r)
	}
	var ev map[string]any
	if json.Unmarshal([]byte(r["body"]), &ev) != nil || ev["event"] != "exit" || ev["game"] != "Jeu" {
		t.Fatalf("corps : %s", r["body"])
	}
}

// TestNotifyManagerUnreachableIsSilent : manager éteint : ni erreur ni attente au-delà du délai de connexion.
func TestNotifyManagerUnreachableIsSilent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := LoadConfig()
	cfg.Peers = []Peer{{Name: "manager", URL: "https://127.0.0.1:1", Token: "jeton", Fingerprint: strings.Repeat("A", 64), Manager: true}}
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	New(Env{}).NotifyManager(map[string]string{"event": "start"})
}
