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

// TestAgentStartInitialises : sans configuration, --agent crée le certificat (aucun jeton).
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

// TestAgentStatusStates : non configuré, non lancé, port pris par un autre processus ; toujours
// code 0.
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
	out.Reset()
	if code := cmdAgentStatus(); code != 0 || !strings.Contains(out.String(), "occupé par un autre processus") {
		t.Errorf("port pris : code %d, sortie %q", code, out.String())
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

// pairPost : POST /v1/pair sur l'agent temporaire (TLS épinglé) ; code HTTP, 0 si injoignable.
func pairPost(t *testing.T, c *http.Client, addr, secret, machine string) int {
	t.Helper()
	req, err := http.NewRequest("POST", "https://"+addr+"/v1/pair", strings.NewReader(`{"name":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("X-Wsq-Machine", machine)
	resp, err := c.Do(req)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// waitPairServed : attend que l'agent temporaire réponde (tout code HTTP).
func waitPairServed(t *testing.T, c *http.Client, addr string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if pairGet(t, c, addr, "", "") != 0 {
			return
		}
	}
	t.Fatal("agent temporaire ne répond pas")
}

// TestPairTempAgentAssociates : l'agent temporaire sert en TLS. Une requête authentifiée d'une machine
// déjà associée n'associe pas, ni un mauvais secret ; le bon secret associe, rend 0, arrête l'agent
// (port fermé), et le code en attente est retiré.
func TestPairTempAgentAssociates(t *testing.T) {
	agentEnv(t)
	cfg, err := agentInit(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	enrollMachine(t, "ancienne-machine-01", "Ancienne", "jeton-ancien")
	const secret = "secret-temp-agent"
	if err := beginPairing(secret, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	ln := listenLocal(t)
	addr := ln.Addr().String()
	out, _ := capteUI(t)
	done := make(chan int, 1)
	go func() { done <- pairRun(cfg, ln, 20*time.Second, nil) }()

	c := pairClient(t)
	waitPairServed(t, c, addr)
	if code := pairGet(t, c, addr, "jeton-ancien", "ancienne-machine-01"); code != http.StatusOK {
		t.Fatalf("machine déjà associée : code %d", code)
	}
	select {
	case rc := <-done:
		t.Fatalf("--pair terminé sur une simple requête authentifiée (code %d)", rc)
	case <-time.After(700 * time.Millisecond):
	}
	if code := pairPost(t, c, addr, "mauvais-secret", "nouvelle-machine-02"); code != http.StatusUnauthorized {
		t.Fatalf("mauvais secret : code %d", code)
	}
	select {
	case rc := <-done:
		t.Fatalf("--pair terminé sur un mauvais secret (code %d)", rc)
	case <-time.After(300 * time.Millisecond):
	}
	if code := pairPost(t, c, addr, secret, "nouvelle-machine-02"); code != http.StatusOK {
		t.Fatalf("bon secret : code %d", code)
	}
	var rc int
	select {
	case rc = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pas d'association après le bon secret")
	}
	if rc != 0 {
		t.Errorf("code %d, attendu 0", rc)
	}
	if !strings.Contains(out.String(), "Associé : test (127.0.0.1)") {
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
	if pathExistsFile(pairPath()) {
		t.Error("code d'association gardé après l'association")
	}
	if last, ok := readLastPair(); !ok || last.ID != "nouvelle-machine-02" || last.Addr != "127.0.0.1" {
		t.Errorf("last-pair.json : %+v (%v)", last, ok)
	}
}

// TestPairTimeout : sans association dans le délai, code 1, agent temporaire arrêté, code retiré.
func TestPairTimeout(t *testing.T) {
	agentEnv(t)
	cfg, err := agentInit(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := beginPairing("secret-delai", time.Minute); err != nil {
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
	if pathExistsFile(pairPath()) {
		t.Error("code d'association gardé après le délai")
	}
}

// TestPairCancelled : annulation (Ctrl-C) : « Association annulée », code 130, agent arrêté, code retiré.
func TestPairCancelled(t *testing.T) {
	agentEnv(t)
	cfg, err := agentInit(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := beginPairing("secret-annule", time.Minute); err != nil {
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
	if pathExistsFile(pairPath()) {
		t.Error("code d'association gardé après l'annulation")
	}
}

// TestPairCommandCtrlC : cmdPair complet ; le crochet d'interruption (installSignals) annule
// l'attente : code 130, pas de sortie du processus, code retiré.
func TestPairCommandCtrlC(t *testing.T) {
	agentEnv(t)
	port := freePort(t)
	out, _ := capteUI(t)
	done := make(chan int, 1)
	go func() { done <- cmdPair([]string{"port", strconv.Itoa(port)}) }()
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
	if pathExistsFile(pairPath()) {
		t.Error("code d'association gardé après Ctrl-C")
	}
}

// TestPairCommandWaitsForAssociation : --pair (commande complète) ne se termine pas sur une simple
// requête authentifiée d'une machine déjà associée ; il se termine sur l'association (POST /v1/pair).
func TestPairCommandWaitsForAssociation(t *testing.T) {
	agentEnv(t)
	port := freePort(t)
	if _, err := agentInit(nil, port); err != nil {
		t.Fatal(err)
	}
	enrollMachine(t, "ancienne-machine-01", "Ancienne", "jeton-ancien")
	oldSecret := pairSecretNew
	pairSecretNew = func() (string, error) { return "secret-cmd-wait", nil }
	t.Cleanup(func() { pairSecretNew = oldSecret })
	out, _ := capteUI(t)
	done := make(chan int, 1)
	go func() { done <- cmdPair([]string{"port", strconv.Itoa(port)}) }()
	for deadline := time.Now().Add(5 * time.Second); interruptHook.Load() == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("cmdPair n'attend pas l'association")
		}
	}
	c := pairClient(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	waitPairServed(t, c, addr)
	if code := pairGet(t, c, addr, "jeton-ancien", "ancienne-machine-01"); code != http.StatusOK {
		t.Fatalf("machine déjà associée : code %d", code)
	}
	select {
	case rc := <-done:
		t.Fatalf("--pair terminé sur une requête d'une machine déjà associée (code %d)", rc)
	case <-time.After(700 * time.Millisecond):
	}
	if code := pairPost(t, c, addr, "secret-cmd-wait", "nouvelle-machine-02"); code != http.StatusOK {
		t.Fatalf("association : code %d", code)
	}
	select {
	case rc := <-done:
		if rc != 0 {
			t.Errorf("code %d, attendu 0", rc)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("--pair ne se termine pas après l'association")
	}
	if !strings.Contains(out.String(), "wsq://secret-cmd-wait@") || !strings.Contains(out.String(), "Associé : test (127.0.0.1)") {
		t.Errorf("sortie : %q", out.String())
	}
	if !strings.Contains(out.String(), "Usage unique") {
		t.Errorf("précision d'usage unique absente : %q", out.String())
	}
}

// TestPairCommandReset : --pair reset retire les machines avant d'attendre : leur jeton est refusé
// dès l'affichage du code.
func TestPairCommandReset(t *testing.T) {
	agentEnv(t)
	port := freePort(t)
	if _, err := agentInit(nil, port); err != nil {
		t.Fatal(err)
	}
	enrollMachine(t, "ancienne-machine-01", "Ancienne", "jeton-ancien")
	out, _ := capteUI(t)
	done := make(chan int, 1)
	go func() { done <- cmdPair([]string{"reset", "port", strconv.Itoa(port)}) }()
	for deadline := time.Now().Add(5 * time.Second); interruptHook.Load() == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("cmdPair n'attend pas l'association")
		}
	}
	c := pairClient(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	waitPairServed(t, c, addr)
	if code := pairGet(t, c, addr, "jeton-ancien", "ancienne-machine-01"); code != http.StatusUnauthorized {
		t.Errorf("jeton d'une machine retirée par --pair reset accepté : code %d", code)
	}
	(*interruptHook.Load())()
	select {
	case rc := <-done:
		if rc != 130 {
			t.Errorf("code %d, attendu 130", rc)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("--pair ne rend pas la main")
	}
	if !strings.Contains(out.String(), "toutes les machines associées sont retirées (Ancienne)") {
		t.Errorf("sortie : %q", out.String())
	}
}

// TestPairForgetReturnsAtOnce : --pair forget retire la machine et rend la main sans attendre
// d'association (ni code, ni « En attente »).
func TestPairForgetReturnsAtOnce(t *testing.T) {
	agentEnv(t)
	enrollMachine(t, "ancienne-machine-01", "Ancienne", "jeton-ancien")
	out, _ := capteUI(t)
	if code := cmdPair([]string{"forget", "ancienne-machine-01"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if strings.Contains(out.String(), "En attente") || strings.Contains(out.String(), "wsq://") {
		t.Errorf("--pair forget lance une association : %q", out.String())
	}
	if _, ok := machineNamed("ancienne-machine-01"); ok {
		t.Error("machine non retirée")
	}
}

// TestPairRejectsStaleAssociation : une association antérieure au début de --pair n'associe pas.
func TestPairRejectsStaleAssociation(t *testing.T) {
	agentEnv(t)
	cfg, err := agentInit(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	stale := lastPair{At: time.Now().Add(-time.Hour), Addr: "192.0.2.9", ID: "ancienne-machine-01", Name: "Vieille"}
	if err := writeJSONAtomic600(lastPairPath(), stale); err != nil {
		t.Fatal(err)
	}
	capteUI(t)
	if code := pairRun(cfg, nil, 300*time.Millisecond, nil); code != 1 {
		t.Errorf("association périmée acceptée (code %d)", code)
	}
}

// TestAgentStatusListsMachines : --agent status liste les machines associées, ou dit qu'il n'y en a pas.
func TestAgentStatusListsMachines(t *testing.T) {
	agentEnv(t)
	if _, err := agentInit(nil, freePort(t)); err != nil {
		t.Fatal(err)
	}
	out, _ := capteUI(t)
	_ = cmdAgentStatus()
	if !strings.Contains(out.String(), "aucune machine associée") {
		t.Errorf("sans machine : %q", out.String())
	}
	enrollMachine(t, "ancienne-machine-01", "Ancienne", "jeton-ancien")
	out.Reset()
	_ = cmdAgentStatus()
	if !strings.Contains(out.String(), "Ancienne (associée le ") {
		t.Errorf("machine non listée : %q", out.String())
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

// TestAgentStopRemovesSystemdUnits : les unités systemd d'une ancienne installation (socket qui
// gardait le port ouvert à chaque session) sont désactivées et supprimées par --agent stop ; status
// les signale tant qu'elles sont là.
func TestAgentStopRemovesSystemdUnits(t *testing.T) {
	agentEnv(t)
	if _, err := agentInit(nil, freePort(t)); err != nil {
		t.Fatal(err)
	}
	orig := agentSystemctl
	t.Cleanup(func() { agentSystemctl = orig })
	var calls []string
	agentSystemctl = func(action string, units ...string) error {
		calls = append(calls, strings.TrimSpace(action+" "+strings.Join(units, " ")))
		return nil
	}
	unit := agentSocketUnit()
	service := filepath.Join(filepath.Dir(unit), "wsquashfs-agent.service")
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{unit, service} {
		if err := os.WriteFile(f, []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := capteUI(t)
	if cmdAgentStatus(); !strings.Contains(out.String(), "Démarrage automatique par systemd") {
		t.Errorf("status : unités non signalées : %q", out.String())
	}
	out.Reset()
	if code := cmdAgentStop(); code != 0 || !strings.Contains(out.String(), "Démarrage automatique par systemd retiré") {
		t.Fatalf("stop : code %d, sortie %q", code, out.String())
	}
	if isFile(unit) || isFile(service) {
		t.Error("unités toujours présentes")
	}
	want := []string{"disable --now wsquashfs-agent.socket", "stop wsquashfs-agent.service", "daemon-reload"}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Errorf("appels systemctl %q, attendu %q", calls, want)
	}
	// Sans unité : rien n'est appelé
	calls = nil
	out.Reset()
	cmdAgentStop()
	if len(calls) != 0 {
		t.Errorf("appels sans unité : %q", calls)
	}
}
