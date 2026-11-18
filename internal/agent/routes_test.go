package agent

// Tests des routes de l'agent qui lancent un sous-processus ou lisent les runners.

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMaintenanceInterruptedBySigterm : client parti pendant une maintenance : le lanceur reçoit
// SIGTERM (il fait son nettoyage), pas SIGKILL.
func TestMaintenanceInterruptedBySigterm(t *testing.T) {
	_, h, _ := agentFixture(t)
	oldDelay := maintenanceStopDelay
	t.Cleanup(func() { maintenanceStopDelay = oldDelay })
	maintenanceStopDelay = 5 * time.Second
	dir := t.TempDir()
	mark, started := filepath.Join(dir, "arret-propre"), filepath.Join(dir, "lance")
	fakeLauncher(t, `trap 'kill $child; : > "`+mark+`"; exit 0' TERM
sleep 30 &
child=$!
: > "`+started+`"
wait $child`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Le client ne part qu'une fois le lanceur lancé et son trap posé (machine chargée : jusqu'à 30 s).
	launched := make(chan bool, 1)
	go func() {
		ok := false
		for i := 0; i < 1500 && !ok; i++ {
			time.Sleep(20 * time.Millisecond)
			ok = pathExistsFile(started)
		}
		launched <- ok
		cancel()
	}()
	req := httptest.NewRequest("POST", "/v1/maintenance", strings.NewReader(`{"action":"clean"}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !<-launched {
		t.Fatalf("le faux lanceur n'a pas démarré : %s", rec.Body.String())
	}
	if !pathExistsFile(mark) {
		t.Errorf("le lanceur n'a pas reçu SIGTERM (tué sans nettoyage) : %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "maintenance interrompue") {
		t.Errorf("interruption non signalée dans la sortie : %s", rec.Body.String())
	}
}

func TestRunnerTarSkipsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root lit tout")
	}
	_, h, _ := agentFixture(t)
	runner := filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/wine/wine-test")
	_ = os.MkdirAll(runner+"/bin", 0o755)
	_ = os.WriteFile(runner+"/bin/wine", []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(runner+"/a-secret.bin", []byte("illisible, plus long que la suite"), 0o000)
	_ = os.WriteFile(runner+"/z-last.txt", []byte("fin"), 0o644)

	rec := do(h, "GET", "/v1/runner/tar?path="+runner, "secret-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	names := map[string]string{}
	tr := tar.NewReader(rec.Body)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("archive illisible après %d entrées : %v", len(names), err)
		}
		data, _ := io.ReadAll(tr)
		names[hdr.Name] = string(data)
	}
	if _, ok := names["wine-test/a-secret.bin"]; ok {
		t.Error("fichier illisible présent dans l'archive")
	}
	if names["wine-test/z-last.txt"] != "fin" {
		t.Errorf("fichier suivant le fichier illisible absent ou faux : %q", names["wine-test/z-last.txt"])
	}
}

// ReadTimeout de l'agent : un traitement plus long que lui (essai de 10 min)
// garde le contexte de sa requête. net/http lève le délai de lecture au
// lancement de sa lecture de fond ; ce test le vérifie avec un délai réduit.
func TestReadTimeoutKeepsLongRequestAlive(t *testing.T) {
	ctxErr := make(chan error, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		_ = json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&v)
		select {
		case <-r.Context().Done():
			ctxErr <- r.Context().Err()
		case <-time.After(700 * time.Millisecond):
			ctxErr <- nil
		}
	}))
	base := testServer().newService(Config{}, false).httpServer()
	srv.Config.ReadHeaderTimeout = base.ReadHeaderTimeout
	srv.Config.ReadTimeout = 200 * time.Millisecond // 60 s en vrai
	srv.Start()
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"rel":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := <-ctxErr; err != nil {
		t.Fatalf("contexte annulé après ReadTimeout : %v", err)
	}
}

// L'agent interrompt --trial par SIGTERM (et non SIGKILL) : le faux --trial
// note le signal reçu.
func TestTrialRouteCancelSendsSIGTERM(t *testing.T) {
	isolateState(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "sig")
	fake := filepath.Join(dir, "fake")
	script := "#!/bin/sh\ntrap 'echo term > " + marker + "; exit 0' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, _, _ := agentFixture(t)
	testExe = fake
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/trial", strings.NewReader(`{"rel":"win/Jeu.wsquashfs","duration":10}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	s.trialRoute(rec, req)
	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("code HTTP %d, attendu 504", rec.Code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("--trial n'a pas reçu SIGTERM à l'interruption")
	}
}
