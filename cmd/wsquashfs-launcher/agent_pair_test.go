package main

// Tests de --agent (lancement en arrière-plan, status, premier plan) et de --pair (association,
// agent temporaire, annulation, délai). Les agents réels écoutent sur 127.0.0.1 ; le lancement
// détaché utilise un faux lanceur (script, ou ce binaire de test en processus d'agent).

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
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

// agentEnv : configuration, état et dossiers isolés (pas de journal ni de config réels).
func agentEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("HOME", dir)
	old := selfPath
	t.Cleanup(func() { selfPath = old })
	return dir
}

// freePort : port TCP libre sur 127.0.0.1 (fermé aussitôt).
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// listenLocal : écoute TCP sur 127.0.0.1 (port choisi), fermée au nettoyage.
func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

// fakeLauncher : script qui remplace le lanceur (selfPath) pendant le test.
func fakeLauncher(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-launcher")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	selfPath = p
	return p
}

func TestParseAgentArgs(t *testing.T) {
	cases := []struct {
		args    []string
		mode    string
		logOn   bool
		wantErr string
	}{
		{args: nil, mode: "start"},
		{args: []string{"stop"}, mode: "stop"},
		{args: []string{"status"}, mode: "status"},
		{args: []string{"run"}, mode: "run"},
		{args: []string{"run", "log"}, mode: "run", logOn: true},
		{args: []string{"log"}, wantErr: "run log"},
		{args: []string{"run", "trace"}, wantErr: "option inconnue pour --agent run"},
		{args: []string{"stop", "x"}, wantErr: "ne prend pas d'argument"},
		{args: []string{"status", "x"}, wantErr: "ne prend pas d'argument"},
		{args: []string{"bidule"}, wantErr: "option inconnue pour --agent"},
	}
	for _, c := range cases {
		got, err := parseAgentArgs(c.args)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%q : erreur %v, attendue contenant %q", c.args, err, c.wantErr)
			}
			continue
		}
		if err != nil || got.mode != c.mode || got.logOn != c.logOn {
			t.Errorf("%q : %+v, %v ; attendu mode %s log %v", c.args, got, err, c.mode, c.logOn)
		}
	}
}

// TestAgentLogWithoutRunIsUsageError : --agent log seul renvoie une erreur (code 1) et non un
// lancement ; la commande de premier plan reste « run log ».
func TestAgentLogWithoutRunIsUsageError(t *testing.T) {
	agentEnv(t)
	out, errs := capteUI(t)
	if code := cmdAgentMain([]string{"log"}); code != 1 {
		t.Errorf("code %d, attendu 1", code)
	}
	if !strings.Contains(errs.String(), "--agent run log") || out.Len() != 0 {
		t.Errorf("erreur %q, sortie %q", errs.String(), out.String())
	}
}

func TestInheritedSocketPredicate(t *testing.T) {
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "1")
	if !inheritedSocket() {
		t.Error("socket héritée non reconnue")
	}
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()+1))
	if inheritedSocket() {
		t.Error("socket d'un autre processus reconnue")
	}
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "")
	if inheritedSocket() {
		t.Error("LISTEN_FDS vide reconnu")
	}
}

// TestAgentStartAlreadyRunning : un agent écoute déjà le port : rien n'est lancé, code 0.
func TestAgentStartAlreadyRunning(t *testing.T) {
	agentEnv(t)
	ln := listenLocal(t)
	port := ln.Addr().(*net.TCPAddr).Port
	if _, err := agentInit(nil, port); err != nil {
		t.Fatal(err)
	}
	fakeLauncher(t, "echo ne-doit-pas-etre-lance; exit 1")
	out, _ := capteUI(t)
	if code := cmdAgentStart(); code != 0 {
		t.Fatalf("code %d", code)
	}
	want := fmt.Sprintf("Agent : déjà en marche sur le port %d", port)
	if !strings.Contains(out.String(), want) {
		t.Errorf("sortie %q, attendu %q", out.String(), want)
	}
}

// TestAgentStartFailsWithLogTail : le lancement n'écoute pas : stFail, fin du journal, code 1 ; et
// sans configuration, --agent l'initialise (comme --pair).
func TestAgentStartFailsWithLogTail(t *testing.T) {
	agentEnv(t)
	old := agentStartWait
	agentStartWait = 300 * time.Millisecond
	t.Cleanup(func() { agentStartWait = old })
	port := freePort(t)
	if _, err := agentInit(nil, port); err != nil {
		t.Fatal(err)
	}
	fakeLauncher(t, "echo « boom au démarrage » >&2; exit 1")
	out, _ := capteUI(t)
	if code := cmdAgentStart(); code != 1 {
		t.Fatalf("code %d, attendu 1 ; sortie %q", code, out.String())
	}
	if !strings.Contains(out.String(), "pas en écoute") || !strings.Contains(out.String(), "boom au démarrage") {
		t.Errorf("sortie : %q", out.String())
	}
}

// TestAgentStartInitialises : sans configuration, --agent crée jeton et certificat.
func TestAgentStartInitialises(t *testing.T) {
	agentEnv(t)
	old := agentStartWait
	agentStartWait = 100 * time.Millisecond
	t.Cleanup(func() { agentStartWait = old })
	// Port libre écrit sans jeton : --agent initialise le reste, sans écouter 8790.
	port := freePort(t)
	if err := saveAgentConfig(agentConfig{Port: port}); err != nil {
		t.Fatal(err)
	}
	fakeLauncher(t, "exit 1")
	out, _ := capteUI(t)
	_ = cmdAgentStart()
	if !agentConfigured() {
		t.Fatalf("configuration absente après --agent : %q", out.String())
	}
	if !strings.Contains(out.String(), "Agent initialisé") {
		t.Errorf("sortie : %q", out.String())
	}
}

// TestAgentStartLaunchesDetached : --agent lance un agent détaché, le rend (code 0) une fois qu'il
// écoute, et le PID annoncé est celui du processus lancé. Le faux agent est ce binaire de test.
func TestAgentStartLaunchesDetached(t *testing.T) {
	agentEnv(t)
	port := freePort(t)
	if _, err := agentInit(nil, port); err != nil {
		t.Fatal(err)
	}
	// Le lanceur est un script qui exécute ce binaire de test sur la seule fonction helper.
	t.Setenv("WSQ_HELPER", "1")
	t.Setenv("WSQ_PORT", strconv.Itoa(port))
	t.Setenv("WSQ_TESTBIN", os.Args[0])
	// « -- --agent » : le processus porte « --agent » dans sa ligne de commande (PID inscrit valide).
	fakeLauncher(t, `exec "$WSQ_TESTBIN" -test.run='^TestAgentHelperProcess$' -- --agent`)
	out, _ := capteUI(t)
	code := cmdAgentStart()
	pidRe := regexp.MustCompile(`pid (\d+)`)
	m := pidRe.FindStringSubmatch(out.String())
	if m != nil {
		pid, _ := strconv.Atoi(m[1])
		// Récolté au nettoyage : un zombie de ce test vu par launcherRunning (trial.go) ferait
		// croire à un jeu en cours dans les tests suivants.
		t.Cleanup(func() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			_, _ = syscall.Wait4(pid, nil, 0, nil)
		})
	}
	if code != 0 {
		t.Fatalf("code %d ; sortie %q", code, out.String())
	}
	if m == nil || !strings.Contains(out.String(), "Agent lancé") || !strings.Contains(out.String(), "--agent stop") {
		t.Errorf("sortie : %q", out.String())
	}
	if !agentListening(port) {
		t.Error("l'agent lancé n'écoute pas après le retour de --agent")
	}
}

// TestAgentHelperProcess : processus d'agent de TestAgentStartLaunchesDetached (n'agit qu'avec
// WSQ_HELPER=1). Écoute le port demandé jusqu'à ce qu'il soit tué.
func TestAgentHelperProcess(t *testing.T) {
	if os.Getenv("WSQ_HELPER") != "1" {
		t.Skip("processus auxiliaire")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("WSQ_PORT"))
	if err != nil {
		os.Exit(2)
	}
	defer ln.Close()
	if err := os.WriteFile(agentPidFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		os.Exit(3)
	}
	time.Sleep(time.Minute)
}

// TestAgentStatusStates : non configuré, non lancé, port pris par un autre processus, socket
// systemd en attente ; toujours code 0.
func TestAgentStatusStates(t *testing.T) {
	agentEnv(t)
	out, _ := capteUI(t)
	if code := cmdAgentStatus(); code != 0 || !strings.Contains(out.String(), "non configuré") {
		t.Errorf("non configuré : code %d, sortie %q", code, out.String())
	}
	port := freePort(t)
	if _, err := agentInit(nil, port); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdAgentStatus(); code != 0 || !strings.Contains(out.String(), "non lancé") {
		t.Errorf("non lancé : code %d, sortie %q", code, out.String())
	}
	ln := listenLocal(t)
	port = ln.Addr().(*net.TCPAddr).Port
	if _, err := agentInit(nil, port); err != nil {
		t.Fatal(err)
	}
	orig := agentSocketActive
	t.Cleanup(func() { agentSocketActive = orig })
	agentSocketActive = func() bool { return false }
	out.Reset()
	if code := cmdAgentStatus(); code != 0 || !strings.Contains(out.String(), "occupé par un autre processus") {
		t.Errorf("port pris, sans socket systemd : code %d, sortie %q", code, out.String())
	}
	// Socket systemd actif (--setup --agent) : systemd tient le port, l'agent démarre à la connexion
	agentSocketActive = func() bool { return true }
	out.Reset()
	if code := cmdAgentStatus(); code != 0 || !strings.Contains(out.String(), "systemd écoute le port") {
		t.Errorf("socket systemd : code %d, sortie %q", code, out.String())
	}
	if !strings.Contains(out.String(), "Journal") {
		t.Errorf("journal absent : %q", out.String())
	}
}

// TestAgentStatusRunningPid : un PID inscrit valide (processus d'agent) est annoncé en marche.
func TestAgentStatusRunningPid(t *testing.T) {
	agentEnv(t)
	port := freePort(t)
	if _, err := agentInit(nil, port); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// Faux agent : « --agent » dans sa ligne de commande (même piège que TestAgentStop).
	fake := execFakeAgent(t)
	if err := os.WriteFile(agentPidFile(), []byte(strconv.Itoa(fake)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := capteUI(t)
	if code := cmdAgentStatus(); code != 0 || !strings.Contains(out.String(), fmt.Sprintf("Agent en marche (pid %d", fake)) {
		t.Errorf("code %d, sortie %q", code, out.String())
	}
}

// execCommandFake : « sh … --agent » qui dort (voir TestAgentStop pour le piège de « ; : »).
func execCommandFake() *exec.Cmd {
	return exec.Command("sh", "-c", "sleep 30; :", "--agent")
}

// execFakeAgent : processus « sh … --agent » qui dort, arrêté au nettoyage. Retourne son PID.
func execFakeAgent(t *testing.T) int {
	t.Helper()
	cmd := execCommandFake()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { _ = cmd.Wait(); close(reaped) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-reaped
	})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", cmd.Process.Pid)); strings.Contains(string(data), "--agent\x00") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("faux agent : « --agent » absent de sa ligne de commande")
		}
	}
	return cmd.Process.Pid
}

// TestSystemdUnitRunsForeground : l'unité systemd lance « --agent run » (premier plan).
func TestSystemdUnitRunsForeground(t *testing.T) {
	agentEnv(t)
	oldDir, oldInstall := agentUnitDir, installDir
	t.Cleanup(func() { agentUnitDir, installDir = oldDir, oldInstall })
	agentUnitDir = t.TempDir()
	installDir = "/opt/wsq"
	if err := writeAgentUnits(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(agentUnitDir, "wsquashfs-agent.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ExecStart=/opt/wsq/wsquashfs-launcher --agent run\n") {
		t.Errorf("unité : %s", data)
	}
}

// TestAgentStatusIgnoresPidOfOtherProcess : un PID inscrit qui n'est pas un agent n'est pas annoncé.
func TestAgentStatusIgnoresPidOfOtherProcess(t *testing.T) {
	agentEnv(t)
	if _, err := agentInit(nil, freePort(t)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentPidFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := capteUI(t)
	_ = cmdAgentStatus()
	if strings.Contains(out.String(), "en marche") {
		t.Errorf("PID d'un autre processus annoncé en marche : %q", out.String())
	}
}

// ── --pair ─────────────────────────────────────────────────────────────

// pairClient : client TLS épinglé sur le certificat de l'agent (configuration courante).
func pairClient(t *testing.T) *http.Client {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(agentDir(), "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		t.Fatal("certificat illisible")
	}
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}},
	}
}

// pairGet : GET /v1/info sur l'agent temporaire ; jeton facultatif ; machine facultative.
func pairGet(t *testing.T, c *http.Client, addr, token, machine string) int {
	t.Helper()
	req, err := http.NewRequest("GET", "https://"+addr+"/v1/info", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if machine != "" {
		req.Header.Set("X-Wsq-Machine", machine)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// TestPairTempAgentAssociates : l'agent temporaire sert en TLS ; une requête NON authentifiée
// n'associe pas ; la première authentifiée associe, rend 0, arrête l'agent (port fermé) et ne
// laisse pas de fichier de PID.
func TestPairTempAgentAssociates(t *testing.T) {
	agentEnv(t)
	cfg, err := agentInit(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ln := listenLocal(t)
	addr := ln.Addr().String()
	out, _ := capteUI(t)
	done := make(chan int, 1)
	go func() { done <- pairRun(cfg, ln, 20*time.Second, nil) }()

	c := pairClient(t)
	// Attend que l'agent temporaire réponde (401 sans jeton).
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if pairGet(t, c, addr, "", "") == http.StatusUnauthorized {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatal("agent temporaire ne répond pas")
	}
	for i := 0; i < 3; i++ {
		if pairGet(t, c, addr, "mauvais", "") != http.StatusUnauthorized {
			t.Fatal("jeton erroné accepté")
		}
	}
	select {
	case rc := <-done:
		t.Fatalf("association sur requête non authentifiée (code %d)", rc)
	case <-time.After(700 * time.Millisecond):
	}
	if _, ok := readLastClient(); ok {
		t.Error("last-client.json écrit sans requête authentifiée")
	}
	if code := pairGet(t, c, addr, cfg.Token, strings.Repeat("a", 32)); code != http.StatusOK {
		t.Fatalf("requête authentifiée : code %d", code)
	}
	var rc int
	select {
	case rc = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pas d'association après une requête authentifiée")
	}
	if rc != 0 {
		t.Errorf("code %d, attendu 0", rc)
	}
	if !strings.Contains(out.String(), "Associé : 127.0.0.1 (synchro des sauvegardes)") {
		t.Errorf("sortie : %q", out.String())
	}
	if !strings.Contains(out.String(), "Pour les synchros suivantes") {
		t.Errorf("astuce d'agent absente : %q", out.String())
	}
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		conn.Close()
		t.Error("l'agent temporaire écoute encore après l'association")
	}
	if pathExistsFile(agentPidFile()) {
		t.Error("fichier de PID écrit par l'agent temporaire")
	}
	client, ok := readLastClient()
	if !ok || client.Addr != "127.0.0.1" || client.Machine != strings.Repeat("a", 32) {
		t.Errorf("last-client.json : %+v (%v)", client, err)
	}
}

// TestPairTimeout : sans association dans le délai, code 1 et agent temporaire arrêté.
func TestPairTimeout(t *testing.T) {
	agentEnv(t)
	cfg, err := agentInit(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ln := listenLocal(t)
	out, _ := capteUI(t)
	if code := pairRun(cfg, ln, 300*time.Millisecond, nil); code != 1 {
		t.Fatalf("code %d, attendu 1", code)
	}
	if !strings.Contains(out.String(), "aucune association en 300ms") {
		t.Errorf("sortie : %q", out.String())
	}
	if conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		conn.Close()
		t.Error("agent temporaire encore en écoute après le délai")
	}
}

// TestPairCancelled : annulation (Ctrl-C) : « Association annulée », code 130, agent arrêté.
func TestPairCancelled(t *testing.T) {
	agentEnv(t)
	cfg, err := agentInit(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ln := listenLocal(t)
	out, _ := capteUI(t)
	cancel := make(chan struct{})
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(cancel)
	}()
	if code := pairRun(cfg, ln, time.Minute, cancel); code != 130 {
		t.Fatalf("code %d, attendu 130", code)
	}
	if !strings.Contains(out.String(), "Association annulée") {
		t.Errorf("sortie : %q", out.String())
	}
}

// TestPairCommandCtrlC : cmdPair complet ; le crochet d'interruption (installSignals) annule
// l'attente : code 130, pas de sortie du processus.
func TestPairCommandCtrlC(t *testing.T) {
	agentEnv(t)
	port := freePort(t)
	out, _ := capteUI(t)
	done := make(chan int, 1)
	go func() { done <- cmdPair([]string{"--port", strconv.Itoa(port)}) }()
	var hook *func()
	for deadline := time.Now().Add(5 * time.Second); hook == nil; time.Sleep(10 * time.Millisecond) {
		hook = interruptHook.Load()
		if time.Now().After(deadline) {
			t.Fatal("cmdPair n'attend pas l'association")
		}
	}
	(*hook)()
	select {
	case rc := <-done:
		if rc != 130 {
			t.Errorf("code %d, attendu 130", rc)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cmdPair ne rend pas la main après Ctrl-C")
	}
	if interruptHook.Load() != nil {
		t.Error("crochet d'interruption non retiré")
	}
	if !strings.Contains(out.String(), "Association annulée") || !strings.Contains(out.String(), "wsq://") {
		t.Errorf("sortie : %q", out.String())
	}
	if agentListening(port) {
		t.Error("agent temporaire encore en écoute")
	}
}

// TestNoteClientThrottle : last-client.json est écrit par l'auth ; un même client n'y écrit pas plus
// d'une fois par seconde, un autre client (machine) l'est tout de suite.
func TestNoteClientThrottle(t *testing.T) {
	_, h, _ := agentFixture(t)
	get := func(machine string) {
		req := httptest.NewRequest("GET", "/v1/info", nil)
		req.Header.Set("Authorization", "Bearer secret-token")
		req.RemoteAddr = "192.0.2.7:4242"
		if machine != "" {
			req.Header.Set("X-Wsq-Machine", machine)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code %d", rec.Code)
		}
	}
	get("")
	first, ok := readLastClient()
	if !ok || first.Addr != "192.0.2.7" || first.Machine != "" {
		t.Fatalf("première trace : %+v (%v)", first, ok)
	}
	get("")
	if again, _ := readLastClient(); !again.At.Equal(first.At) {
		t.Error("même client réécrit dans la seconde")
	}
	get(strings.Repeat("b", 32))
	if other, _ := readLastClient(); other.Machine != strings.Repeat("b", 32) {
		t.Errorf("autre machine non notée : %+v", other)
	}
}

// TestNoteClientNotForRefusedRequests : une requête refusée (jeton) ne touche pas last-client.json.
func TestNoteClientNotForRefusedRequests(t *testing.T) {
	_, h, _ := agentFixture(t)
	req := httptest.NewRequest("GET", "/v1/info", nil)
	req.Header.Set("Authorization", "Bearer faux")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if _, ok := readLastClient(); ok {
		t.Error("requête refusée notée comme association")
	}
}

// TestPairRejectsStaleClient : une trace antérieure au lancement de --pair n'associe pas.
func TestPairRejectsStaleClient(t *testing.T) {
	agentEnv(t)
	cfg, err := agentInit(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	stale := lastClient{At: time.Now().Add(-time.Hour), Addr: "192.0.2.9", Machine: strings.Repeat("c", 32)}
	if err := writeLastClient(stale); err != nil {
		t.Fatal(err)
	}
	capteUI(t)
	if code := pairRun(cfg, nil, 300*time.Millisecond, nil); code != 1 {
		t.Errorf("trace périmée acceptée (code %d)", code)
	}
}

// TestDurationText : durées des messages de --pair.
func TestDurationText(t *testing.T) {
	if got := durationText(10 * time.Minute); got != "10 min" {
		t.Errorf("10 min : %q", got)
	}
	if got := durationText(300 * time.Millisecond); got != "300ms" {
		t.Errorf("300 ms : %q", got)
	}
}

// TestAgentStopAndStartWithSystemdSocket : --agent stop arrête le socket et le service (le port
// est libéré) ; --agent relance le socket installé plutôt qu'un agent détaché.
func TestAgentStopAndStartWithSystemdSocket(t *testing.T) {
	agentEnv(t)
	if _, err := agentInit(nil, freePort(t)); err != nil {
		t.Fatal(err)
	}
	origActive, origCtl := agentSocketActive, agentSystemctl
	t.Cleanup(func() { agentSocketActive, agentSystemctl = origActive, origCtl })
	active := true
	var calls []string
	agentSocketActive = func() bool { return active }
	agentSystemctl = func(action string, units ...string) error {
		calls = append(calls, action+" "+strings.Join(units, " "))
		active = action == "start"
		return nil
	}
	out, _ := capteUI(t)
	if code := cmdAgentStop(); code != 0 || !strings.Contains(out.String(), "le port est libéré") {
		t.Fatalf("stop : code %d, sortie %q", code, out.String())
	}
	if len(calls) != 1 || calls[0] != "stop wsquashfs-agent.socket wsquashfs-agent.service" {
		t.Fatalf("stop : appels systemctl %q", calls)
	}
	// Socket installé et arrêté : --agent le relance
	unit := agentSocketUnit()
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte("[Socket]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdAgentStart(); code != 0 || !strings.Contains(out.String(), "systemd écoute le port") {
		t.Fatalf("start : code %d, sortie %q", code, out.String())
	}
	if len(calls) != 2 || calls[1] != "start wsquashfs-agent.socket" || !active {
		t.Fatalf("start : appels systemctl %q", calls)
	}
}
