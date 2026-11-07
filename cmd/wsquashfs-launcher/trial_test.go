package main

// Tests de --trial et de l'agent sur une vraie connexion : interruption de
// l'essai (SIGTERM), délai de lecture du corps, détection d'un lanceur actif.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

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
	base := (&agentServer{}).httpServer()
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

// SIGTERM pendant l'essai : le lanceur lancé (Setsid, hors du groupe) reçoit
// SIGTERM, son code est relevé après sa fin, et le rapport le signale.
func TestTrialInterruptedStopsLauncher(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "Essai.wsquashfs")
	if err := os.WriteFile(image, []byte("hsqs"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "stopped")
	fake := filepath.Join(dir, "fake-launcher")
	script := "#!/bin/sh\ntrap 'echo term > " + marker + "; exit 7' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := selfPath
	selfPath = fake
	defer func() { selfPath = old }()

	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	go func() {
		time.Sleep(time.Second)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	start := time.Now()
	runTrial(image, 120, "", "")
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)

	var rep trialReport
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("rapport illisible : %v\n%s", err, out)
	}
	if rep.Error != "interrupted" {
		t.Errorf("error = %q, attendu interrupted", rep.Error)
	}
	if rep.Code != 7 {
		t.Errorf("code = %d, attendu 7 (lanceur attendu jusqu'à sa fin)", rep.Code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("le lanceur n'a pas reçu SIGTERM")
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("arrêt trop long : %v", d)
	}
}

// L'agent interrompt --trial par SIGTERM (et non SIGKILL) : le faux --trial
// note le signal reçu.
func TestTrialRouteCancelSendsSIGTERM(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "sig")
	fake := filepath.Join(dir, "fake")
	script := "#!/bin/sh\ntrap 'echo term > " + marker + "; exit 0' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s, _, _ := agentFixture(t)
	selfPath = fake
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

func TestLaunchesGame(t *testing.T) {
	cases := map[string]bool{
		"Jeu.wsquashfs":                true,
		"--mount Jeu.wsquashfs":        true,
		"--umount Jeu.wsquashfs":       true,
		"--exec Jeu.wsquashfs cmd.exe": true,
		"--doctor --json":              false,
		"--list":                       false,
		"--agent log":                  false,
		"--trial Jeu.wsquashfs 60":     false,
		"--info Jeu.wsquashfs":         false,
		"--wine tkg --list":            false,
		"--option-inconnue":            true,
	}
	for line, want := range cases {
		if got := launchesGame(strings.Fields(line)); got != want {
			t.Errorf("launchesGame(%q) = %v, attendu %v", line, got, want)
		}
	}
	for comm, want := range map[string]bool{
		"wsquashfs-launc": true, "wsquashfs-launcher": true, "w": false, "wsquashfs": false, "": false,
	} {
		if got := isLauncherComm(comm); got != want {
			t.Errorf("isLauncherComm(%q) = %v, attendu %v", comm, got, want)
		}
	}
}

func TestEnvValue(t *testing.T) {
	env := []string{"DISPLAY=:1", "PATH=/bin", "DISPLAY=:0"}
	if v := envValue(env, "DISPLAY"); v != ":0" {
		t.Errorf("DISPLAY = %q, attendu :0 (la dernière valeur l'emporte)", v)
	}
	if v := envValue(env, "WAYLAND_DISPLAY"); v != "" {
		t.Errorf("WAYLAND_DISPLAY = %q, attendu vide", v)
	}
}

// Deux réponses par un tube : la seconde n'est pas perdue.
func TestReadLineKeepsBufferedInput(t *testing.T) {
	r, w, _ := os.Pipe()
	_, _ = w.WriteString("o\nn\n")
	w.Close()
	oldIn, oldReader := os.Stdin, stdinReader
	os.Stdin, stdinReader = r, bufio.NewReader(r)
	defer func() { os.Stdin, stdinReader = oldIn, oldReader }()
	if a, b := readLine(), readLine(); a != "o" || b != "n" {
		t.Errorf("lignes lues %q, %q ; attendu o, n", a, b)
	}
}

// installTree : une copie ratée laisse l'ancienne version en place.
func TestInstallTreeKeepsOldOnFailure(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "runner")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dst, "v"), []byte("ancien"), 0o644)
	if installTree(filepath.Join(dir, "absent"), dst) == nil {
		t.Fatal("copie d'une source absente acceptée")
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "v")); !bytes.Equal(data, []byte("ancien")) {
		t.Fatal("ancienne version perdue après un échec")
	}
	src := filepath.Join(dir, "src")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "v"), []byte("nouveau"), 0o644)
	if err := installTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "v")); !bytes.Equal(data, []byte("nouveau")) {
		t.Fatalf("contenu %q, attendu nouveau", data)
	}
	if pathExists(dst+".new") || pathExists(dst+".old") {
		t.Error("dossiers intermédiaires restés")
	}
}

// Fin de partie et cleanup (signal) arrêtent ATTACH en même temps : un seul
// close, pas de panique.
func TestStopAttachCommandConcurrent(t *testing.T) {
	attachMarker = "WSQUASHFS_ATTACH=999999999"
	attachStop = make(chan struct{})
	attachDone = make(chan struct{})
	close(attachDone)
	start := make(chan struct{})
	finished := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		go func() {
			<-start
			stopAttachCommand()
			finished <- struct{}{}
		}()
	}
	close(start)
	for i := 0; i < 8; i++ {
		<-finished
	}
	if attachStop != nil {
		t.Error("attachStop non remis à nil")
	}
}
