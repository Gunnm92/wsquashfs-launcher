package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serve(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestInfoReportsActivity(t *testing.T) {
	svc, h, _ := agentFixture(t)
	svc.env.Build = func() string { return "abc123def456" }
	svc.env.Playing = func() []Playing { return []Playing{{Game: "Crazy Taxi 3", Since: 1700000000}} }
	svc.env.Controllers = func() []Controller { return []Controller{{Kind: "dualsense", Name: "DualSense", Bus: "bluetooth"}} }
	rec := serve(h, "GET", "/v1/info", "")
	var v struct {
		Version     string       `json:"version"`
		Build       string       `json:"build"`
		Playing     []Playing    `json:"playing"`
		Controllers []Controller `json:"controllers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Version != "11" || v.Build != "abc123def456" || len(v.Playing) != 1 || v.Playing[0].Game != "Crazy Taxi 3" ||
		len(v.Controllers) != 1 || v.Controllers[0].Kind != "dualsense" {
		t.Fatalf("info : %s", rec.Body.String())
	}
}

func TestInfoActivityEmptyWithoutEnv(t *testing.T) {
	_, h, _ := agentFixture(t)
	body := serve(h, "GET", "/v1/info", "").Body.String()
	if !strings.Contains(body, `"playing":[]`) || !strings.Contains(body, `"controllers":[]`) || !strings.Contains(body, `"build":""`) {
		t.Fatalf("listes vides attendues : %s", body)
	}
}

func TestUpdateRestartsOnlyWhenVersionChanges(t *testing.T) {
	svc, h, _ := agentFixture(t)
	oldDelay := restartDelay
	t.Cleanup(func() { restartDelay = oldDelay })
	restartDelay = 0
	fakeLauncher(t, `[ "$1" = --update ] || exit 9; echo "Lanceur mis à jour"`)
	builds := []string{"ancienne", "nouvelle"}
	var calls, restarts atomic.Int32
	svc.env.Build = func() string { return builds[min(int(calls.Add(1))-1, 1)] }
	restarted := make(chan bool, 1)
	svc.env.Restart = func() error { restarts.Add(1); restarted <- true; return nil }
	rec := serve(h, "POST", "/v1/update", "")
	var v struct {
		Code    int    `json:"code"`
		Output  string `json:"output"`
		Before  string `json:"before"`
		After   string `json:"after"`
		Restart bool   `json:"restart"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if v.Code != 0 || v.Before != "ancienne" || v.After != "nouvelle" || !v.Restart || !strings.Contains(v.Output, "mis à jour") {
		t.Fatalf("réponse : %+v", v)
	}
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("agent non relancé")
	}

	// Déjà à jour : pas de relance.
	builds = []string{"pareille", "pareille"}
	calls.Store(0)
	rec = serve(h, "POST", "/v1/update", "")
	if !strings.Contains(rec.Body.String(), `"restart":false`) {
		t.Fatalf("sans changement, pas de relance : %s", rec.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
	if restarts.Load() != 1 {
		t.Fatalf("relances : %d", restarts.Load())
	}
}

func TestUpdateFailureKeepsAgent(t *testing.T) {
	svc, h, _ := agentFixture(t)
	fakeLauncher(t, `echo "somme de contrôle incorrecte"; exit 1`)
	svc.env.Build = func() string { return "pareille" }
	svc.env.Restart = func() error { t.Error("relance après un échec"); return nil }
	body := serve(h, "POST", "/v1/update", "").Body.String()
	if !strings.Contains(body, `"code":1`) || !strings.Contains(body, `"restart":false`) {
		t.Fatalf("échec attendu : %s", body)
	}
}

func TestUpdateRefusedDuringUpload(t *testing.T) {
	svc, h, _ := agentFixture(t)
	svc.env.Build = func() string { return "x" }
	svc.gamesMu.Lock()
	svc.uploads = map[string]bool{"Jeu": true}
	svc.gamesMu.Unlock()
	if rec := serve(h, "POST", "/v1/update", ""); rec.Code != http.StatusConflict {
		t.Fatalf("409 attendu : %d %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateNeedsToken(t *testing.T) {
	_, h, _ := agentFixture(t)
	req := httptest.NewRequest("POST", "/v1/update", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("401 attendu : %d", rec.Code)
	}
}
