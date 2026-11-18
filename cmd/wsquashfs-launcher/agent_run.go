package main

// Commandes --agent, --agent run | stop | status : lancement de l'agent en arrière-plan,
// arrêt par fichier de PID, état, et service systemd (unités, socket).

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"wsquashfs-launcher/internal/ui"
)

// agentListening : un agent (ou une socket systemd) accepte les connexions.
func agentListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// waitAgentUp : vrai si l'agent lancé (pid) écoute et a inscrit son PID avant le délai.
// L'inscription suit l'écoute : sans elle, « --agent status » verrait une socket sans processus.
func waitAgentUp(port, pid int, d time.Duration) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(100 * time.Millisecond) {
		if got, ok := runningAgentPid(); ok && got == pid && agentListening(port) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// agentLogPath : journal de l'agent lancé en arrière-plan (--agent).
func agentLogPath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	return filepath.Join(base, "wsquashfs", "agent.log")
}

// agentLogSize : taille du journal (0 s'il n'existe pas) : début de ce qu'un lancement écrit.
func agentLogSize() int64 {
	if st, err := os.Stat(agentLogPath()); err == nil {
		return st.Size()
	}
	return 0
}

// agentLogSince : dernières lignes du journal écrites après offset.
func agentLogSince(offset int64) []string {
	f, err := os.Open(agentLogPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return lines
}

// startAgentDetached : lance « wsquashfs-launcher --agent run log » hors du
// terminal (nouvelle session, journal dans agentLogPath). Retourne son PID.
func startAgentDetached() (int, error) {
	logPath := agentLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer log.Close()
	cmd := exec.Command(conf.selfPath, "--agent", "run", "log")
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// agentStartWait : délai laissé à l'agent lancé pour écouter. Variable pour les tests.
var agentStartWait = 3 * time.Second

// cmdAgentStart : --agent. Lance l'agent en arrière-plan et rend la main. Sans
// configuration, l'initialise comme --pair (jeton, certificat).
func cmdAgentStart() int {
	if !agentConfigured() {
		if _, err := agentInit(nil, 0); err != nil {
			ui.Error("agent non initialisé : " + err.Error())
			return 1
		}
		ui.StatusLine(ui.OK, "Agent initialisé (certificat dans "+agentDir()+")")
		ui.Note("Code d'association : wsquashfs-launcher --pair")
	}
	removeAgentUnits()
	cfg := loadAgentConfig()
	if agentListening(cfg.Port) {
		msg := fmt.Sprintf("Agent : déjà en marche sur le port %d", cfg.Port)
		if pid, ok := runningAgentPid(); ok {
			msg += fmt.Sprintf(" (pid %d)", pid)
		}
		ui.StatusLine(ui.Skip, msg)
		return 0
	}
	before := agentLogSize()
	pid, err := startAgentDetached()
	if err != nil {
		ui.StatusLine(ui.Fail, "Agent : lancement impossible : "+err.Error())
		return 1
	}
	if !waitAgentUp(cfg.Port, pid, agentStartWait) {
		ui.StatusLine(ui.Fail, fmt.Sprintf("Agent (pid %d) : pas en écoute sur le port %d après %s, fin du journal :",
			pid, cfg.Port, durationText(agentStartWait)))
		tail := agentLogSince(before)
		if len(tail) == 0 {
			tail = []string{"(journal vide)"}
		}
		ui.Note(tail...)
		return 1
	}
	ui.StatusLine(ui.OK, fmt.Sprintf("Agent lancé (pid %d, port %d), journal : %s", pid, cfg.Port, agentLogPath()))
	ui.Note("Arrêter : wsquashfs-launcher --agent stop")
	return 0
}

// runningAgentPid : PID inscrit dans agentPidFile, s'il est bien un agent.
func runningAgentPid() (int, bool) {
	data, err := os.ReadFile(agentPidFile())
	if err != nil {
		return 0, false
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid, pid > 0 && isAgentProcess(pid)
}

// cmdAgentStatus : --agent status. Code 0 dans tous les cas.
func cmdAgentStatus() int {
	if !agentConfigured() {
		ui.StatusLine(ui.Skip, "Agent : non configuré (« wsquashfs-launcher --agent » l'initialise)")
		return 0
	}
	cfg := loadAgentConfig()
	if pid, ok := runningAgentPid(); ok {
		ui.StatusLine(ui.OK, fmt.Sprintf("Agent en marche (pid %d, port %d)", pid, cfg.Port))
	} else if agentListening(cfg.Port) {
		ui.StatusLine(ui.Info, fmt.Sprintf("Agent : port %d occupé par un autre processus (--pair en cours ?)", cfg.Port))
	} else {
		ui.StatusLine(ui.Skip, fmt.Sprintf("Agent : non lancé (port %d fermé)", cfg.Port))
	}
	if isFile(agentSocketUnit()) {
		ui.Note("Démarrage automatique par systemd d'une ancienne installation encore présent (port ouvert",
			"à chaque session) : « wsquashfs-launcher --agent stop » le retire.")
	}
	if len(cfg.Machines) == 0 {
		ui.KV(0, ui.Pair{"Machines", "aucune machine associée"})
	} else {
		items := make([]string, 0, len(cfg.Machines))
		for _, m := range cfg.Machines {
			items = append(items, fmt.Sprintf("%s (associée le %s)", m.Name, m.Paired.Local().Format("2006-01-02 15:04")))
		}
		ui.List("Machines associées", items)
	}
	ui.KV(0, ui.Pair{"Journal", agentLogPath()})
	return 0
}

// agentPidFile : PID de l'agent en marche.
func agentPidFile() string {
	return filepath.Join(agentDir(), "agent.pid")
}

// agentStopWait : attente de la fin de l'agent après SIGTERM (--agent stop). L'agent finit
// ses requêtes en cours pendant agentStopGrace au plus, qui doit rester en dessous.
const agentStopWait = 5 * time.Second

// cmdAgentStop : --agent stop. Arrête l'agent (SIGTERM) pour le PID inscrit
// dans agentPidFile, seulement si ce processus est bien un agent (sinon, le
// fichier est périmé et retiré). Rien à arrêter : code 0.
func cmdAgentStop() int {
	removeAgentUnits()
	data, err := os.ReadFile(agentPidFile())
	if err != nil {
		ui.StatusLine(ui.Skip, "Agent : non lancé")
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid <= 0 || !isAgentProcess(pid) {
		_ = os.Remove(agentPidFile())
		ui.StatusLine(ui.Skip, "Agent : non lancé (fichier de PID périmé, retiré)")
		return 0
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		ui.StatusLine(ui.Fail, fmt.Sprintf("Agent (pid %d) : arrêt impossible (%v)", pid, err))
		return 1
	}
	for deadline := time.Now().Add(agentStopWait); ; time.Sleep(100 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			_ = os.Remove(agentPidFile())
			ui.StatusLine(ui.OK, fmt.Sprintf("Agent arrêté (pid %d)", pid))
			return 0
		}
		if time.Now().After(deadline) {
			break
		}
	}
	ui.StatusLine(ui.Fail, fmt.Sprintf("Agent (pid %d) toujours en cours après %s", pid, durationText(agentStopWait)))
	return 1
}

// removeAgentUnits : retire le démarrage automatique par systemd qu'installaient les anciennes
// versions de « --setup --agent », supprimé (socket qui gardait le port ouvert à chaque session). L'agent ne
// tourne plus que lancé à la main (--agent). Seulement si l'unité est dans la configuration
// courante : un HOME isolé (tests, scénarios) ne touche jamais la vraie session. Vrai si retiré.
func removeAgentUnits() bool {
	socket := agentSocketUnit()
	if !isFile(socket) {
		return false
	}
	_ = agentSystemctl("disable", "--now", "wsquashfs-agent.socket")
	_ = agentSystemctl("stop", "wsquashfs-agent.service")
	service := filepath.Join(filepath.Dir(socket), "wsquashfs-agent.service")
	if os.Remove(socket) != nil {
		return false
	}
	_ = os.Remove(service)
	_ = agentSystemctl("daemon-reload")
	ui.StatusLine(ui.OK, "Démarrage automatique par systemd retiré : le port n'est plus ouvert en dehors de --agent")
	return true
}

// agentSystemctl : systemctl --user <action> <unités> (variable : remplacée dans les tests).
var agentSystemctl = func(action string, units ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", append([]string{"--user", action}, units...)...).CombinedOutput()
	if err != nil && len(out) > 0 {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return err
}

// agentSocketUnit : fichier du socket systemd installé par les anciennes versions (« --setup --agent », supprimé).
func agentSocketUnit() string {
	base := configHome()
	return filepath.Join(base, "systemd", "user", "wsquashfs-agent.socket")
}

// isAgentProcess : le processus est un « wsquashfs-launcher --agent ».
func isAgentProcess(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	for _, a := range args {
		if a == "--agent" {
			return true
		}
	}
	return false
}

// prepareAgent : routes et TLS de l'agent, sans écoute. Erreur : agent non
// initialisé (certificat absent) ou certificat illisible. Sans machine associée, l'agent
// tourne quand même : il ne refuse que ce qui n'est pas POST /v1/pair.
func prepareAgent(cfg agentConfig, logOn bool) (*agentServer, *tls.Config, error) {
	if !agentConfigured() {
		return nil, nil, errors.New("agent non initialisé : « wsquashfs-launcher --agent » d'abord")
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(agentDir(), "cert.pem"), filepath.Join(agentDir(), "key.pem"))
	if err != nil {
		return nil, nil, fmt.Errorf("certificat illisible : %w", err)
	}
	tlsConf := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	return newAgentServer(cfg, logOn), tlsConf, nil
}

// agentListener : écoute sur le port configuré.
func agentListener(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf(":%d", port))
}

// agentStopGrace : délai laissé aux requêtes en cours quand l'agent reçoit SIGTERM ou SIGINT.
// Doit tenir dans l'attente de « --agent stop » (agentStopWait). Variable pour les tests.
var agentStopGrace = 3 * time.Second

// serveAgent : premier plan (--agent run, et l'agent lancé par --agent). Écoute TLS, PID
// pour « --agent stop ». logOn : trace des requêtes sur la sortie d'erreur.
// SIGTERM ou SIGINT : arrêt propre, code 0. Le port est fermé tout de suite, les requêtes en
// cours ont agentStopGrace pour finir (un remplacement de sauvegardes commencé va au bout) ;
// passé ce délai elles sont coupées (un envoi encore en lecture : rien n'a été remplacé).
func serveAgent(logOn bool) int {
	// Posé avant l'écoute et le fichier de PID : un signal ne sort plus sans arrêt propre
	// (installSignals appelle ce crochet au lieu de sortir).
	stop := make(chan struct{})
	var once sync.Once
	hook := func() { once.Do(func() { close(stop) }) }
	interruptHook.Store(&hook)
	defer interruptHook.Store(nil)
	cfg := loadAgentConfig()
	ag, tlsConf, err := prepareAgent(cfg, logOn)
	if err != nil {
		ui.Error(err.Error())
		return 1
	}
	ln, err := agentListener(cfg.Port)
	if err != nil {
		ui.Error("écoute impossible : " + err.Error())
		return 1
	}
	ui.Err(ui.OK.Mark() + fmt.Sprintf(" wsquashfs-agent en écoute sur le port %d", cfg.Port))
	httpSrv := ag.httpServer()
	// Pour « --agent stop » : le PID de cet agent (retiré à l'arrêt propre).
	if err := os.WriteFile(agentPidFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err == nil {
		defer os.Remove(agentPidFile())
	}
	served := make(chan error, 1)
	go func() { served <- httpSrv.Serve(tls.NewListener(ln, tlsConf)) }()
	select {
	case err := <-served:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			ui.Error("arrêt : " + err.Error())
			return 1
		}
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), agentStopGrace)
		defer cancel()
		if err := httpSrv.Shutdown(ctx); err != nil {
			_ = httpSrv.Close()
		}
		<-served
		ui.Err(ui.OK.Mark() + " wsquashfs-agent arrêté")
	}
	return 0
}
