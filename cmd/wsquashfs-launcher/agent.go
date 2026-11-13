package main

// Agent pour wsquashfs-manager : serveur HTTPS (jeton, certificat épinglé)
// qui expose les opérations du lanceur sur la machine de jeu. Pas
// d'exécution de commande fournie par le client : chaque route correspond à
// une action fixe du lanceur, lancée en sous-processus.
//
// Configuration : ~/.config/wsquashfs/agent/ (agent.json, cert.pem, key.pem).
//
// Commandes :
//   --agent            lance l'agent en arrière-plan et rend la main (le
//                      configure d'abord, comme --pair, si besoin)
//   --agent run [log]  premier plan : systemd (ExecStart) et débogage
//   --agent stop       arrête l'agent lancé (fichier de PID)
//   --agent status     en marche ou non, pid, port, journal
//   --pair             affiche le code wsq://…, attend l'association (première
//                      requête authentifiée) et se ferme ; sans agent en marche,
//                      sert un agent temporaire le temps de l'attente.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	agentVersion     = "4"
	agentDefaultPort = 8790
	agentIdleExit    = 60 * time.Second // sans requête, quand systemd tient la socket
)

// agentConfig : agent.json.
type agentConfig struct {
	Port    int      `json:"port"`
	Token   string   `json:"token"`
	Roms    []string `json:"roms"`
	Session string   `json:"session_process"`
}

// agentDir : dossier de configuration de l'agent.
func agentDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(base, "wsquashfs", "agent")
}

func loadAgentConfig() agentConfig {
	var cfg agentConfig
	if data, err := os.ReadFile(filepath.Join(agentDir(), "agent.json")); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg.Port == 0 {
		cfg.Port = agentDefaultPort
	}
	if cfg.Session == "" {
		cfg.Session = "emulationstation"
	}
	return cfg
}

// saveAgentConfig : écriture atomique, droits 600.
func saveAgentConfig(cfg agentConfig) error {
	if err := os.MkdirAll(agentDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(agentDir(), "agent.json.tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(agentDir(), "agent.json"))
}

// newToken : 32 octets aléatoires, base64url.
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// localAddresses : adresses IP de la machine, sans boucle locale ni lien local.
func localAddresses() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, ipnet.IP.String())
	}
	return out
}

// ensureAgentCert : certificat auto-signé ECDSA P-256 (10 ans), créé une fois.
func ensureAgentCert() error {
	certPath := filepath.Join(agentDir(), "cert.pem")
	keyPath := filepath.Join(agentDir(), "key.pem")
	if isFile(certPath) && isFile(keyPath) {
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "wsquashfs-agent"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	if host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
	for _, a := range localAddresses() {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.ParseIP(a))
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(agentDir(), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	return os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
}

// certFingerprint : SHA-256 du certificat (DER), en hexadécimal majuscule.
func certFingerprint() (string, error) {
	data, err := os.ReadFile(filepath.Join(agentDir(), "cert.pem"))
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", fmt.Errorf("certificat illisible")
	}
	sum := sha256.Sum256(block.Bytes)
	return strings.ToUpper(hex.EncodeToString(sum[:])), nil
}

// pairCode : code d'association wsq://JETON@HÔTE:PORT#EMPREINTE.
func pairCode(cfg agentConfig, host string, fp string) string {
	return fmt.Sprintf("wsq://%s@%s:%d#%s", cfg.Token, host, cfg.Port, fp)
}

// agentInit : configuration, jeton et certificat (créés une fois ; --roms et
// --port remplacent les valeurs existantes).
func agentInit(roms []string, port int) (agentConfig, error) {
	cfg := loadAgentConfig()
	if cfg.Token == "" {
		tok, err := newToken()
		if err != nil {
			return cfg, err
		}
		cfg.Token = tok
	}
	if port != 0 {
		cfg.Port = port
	}
	if len(roms) > 0 {
		cfg.Roms = nil
		for _, r := range roms {
			if abs, err := filepath.Abs(r); err == nil {
				cfg.Roms = append(cfg.Roms, abs)
			}
		}
	}
	if len(cfg.Roms) == 0 {
		cfg.Roms = []string{agentDefaultRoms()}
	}
	if err := saveAgentConfig(cfg); err != nil {
		return cfg, err
	}
	return cfg, ensureAgentCert()
}

// cmdPair : --pair [--reset] [--roms DIR]... [--port N]. Initialise si besoin,
// affiche le code d'association, puis attend l'association (pairRun).
func cmdPair(args []string) int {
	var roms []string
	port := 0
	reset := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--reset":
			reset = true
		case "--roms":
			dir := argAt(args, i+1)
			if dir == "" || strings.HasPrefix(dir, "--") {
				die("--roms attend un dossier")
			}
			roms = append(roms, dir)
			i++
		case "--port":
			n, err := strconv.Atoi(argAt(args, i+1))
			if err != nil || n < 1 || n > 65535 {
				die("--port attend un numéro de port (1 à 65535)")
			}
			port = n
			i++
		default:
			die("option inconnue pour --pair : " + args[i])
		}
	}
	cfg := loadAgentConfig()
	if reset {
		tok, err := newToken()
		if err != nil {
			die("jeton impossible : " + err.Error())
		}
		cfg.Token = tok
		if err := saveAgentConfig(cfg); err != nil {
			die("configuration impossible : " + err.Error())
		}
	}
	cfg, err := agentInit(roms, port)
	if err != nil {
		die("agent non initialisé : " + err.Error())
	}
	code, err := currentPairCode(cfg)
	if err != nil {
		die("certificat illisible : " + err.Error())
	}
	uiTitle("Association avec wsquashfs-manager")
	uiNote("Colle ce code sur l'autre machine (wsquashfs-launcher --sync wsq://…)",
		"ou dans wsquashfs-manager (Réglages → Machine de jeu, ou onglet Sauvegardes) :")
	uiOut("")
	uiCode(code)
	uiOut("")
	// Agent : celui qui tourne déjà (systemd, --agent), sinon un agent
	// temporaire servi par ce processus, arrêté après l'association.
	var ln net.Listener
	state := fmt.Sprintf("déjà en marche sur le port %d", cfg.Port)
	if !agentListening(cfg.Port) {
		ln, err = net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
		if err != nil {
			die(fmt.Sprintf("agent temporaire impossible sur le port %d : %s", cfg.Port, err.Error()))
		}
		state = fmt.Sprintf("temporaire (port %d), arrêté après l'association", cfg.Port)
	}
	uiKV(0, kv{"Autres adresses", strings.Join(localAddresses(), ", ")}, kv{"Dossiers roms/", strings.Join(cfg.Roms, ", ")}, kv{"Agent", state})
	// Ctrl-C : annulation propre (installSignals appelle ce crochet, sans sortir).
	cancel := make(chan struct{})
	var once sync.Once
	hook := func() { once.Do(func() { close(cancel) }) }
	interruptHook.Store(&hook)
	defer interruptHook.Store(nil)
	return pairRun(cfg, ln, pairTimeout, cancel)
}

// currentPairCode : code wsq:// de cette machine (hôte : première adresse locale).
func currentPairCode(cfg agentConfig) (string, error) {
	fp, err := certFingerprint()
	if err != nil {
		return "", err
	}
	host := "localhost"
	if addrs := localAddresses(); len(addrs) > 0 {
		host = addrs[0]
	} else if h, err := os.Hostname(); err == nil {
		host = h
	}
	return pairCode(cfg, host, fp), nil
}

// pairTimeout : délai d'attente de l'association (--pair). Variable pour les tests.
var pairTimeout = 10 * time.Minute

// interruptHook : appelé à la place de la sortie quand un signal arrive
// (installSignals), tant que --pair attend l'association.
var interruptHook atomic.Pointer[func()]

// pairResult : issue de l'attente d'association.
type pairResult int

const (
	pairDone pairResult = iota
	pairTimedOut
	pairCancelled
)

// pairRun : attend l'association de cette machine et rend le code de --pair :
// 0 associé, 1 délai dépassé ou agent temporaire impossible, 130 annulé.
// ln : écoute d'un agent temporaire à servir (nil : un agent tourne déjà).
func pairRun(cfg agentConfig, ln net.Listener, timeout time.Duration, cancel <-chan struct{}) int {
	stop := func() {}
	if ln != nil {
		s, err := serveTempAgent(cfg, ln)
		if err != nil {
			_ = ln.Close()
			uiStatusLine(stFail, "Agent temporaire impossible : "+err.Error())
			return 1
		}
		stop = s
	}
	since := time.Now()
	uiStatusLine(stInfo, "En attente de l'association… (Ctrl-C pour annuler)")
	client, res := waitAssociation(since, timeout, cancel)
	stop()
	switch res {
	case pairCancelled:
		uiStatusLine(stSkip, "Association annulée")
		return 130
	case pairTimedOut:
		uiStatusLine(stFail, "Association : aucune association en "+durationText(timeout))
		return 1
	}
	msg := "Associé : " + client.Addr
	if client.Machine != "" {
		msg += " (synchro des sauvegardes)"
	}
	uiStatusLine(stOK, msg)
	if ln != nil {
		uiNote("Pour les synchros suivantes, l'agent doit tourner sur cette machine :",
			"wsquashfs-launcher --agent (ou --setup --agent pour le démarrer avec la session)")
	}
	return 0
}

// waitAssociation : attend une requête authentifiée reçue depuis since
// (last-client.json, écrit par l'agent), jusqu'à timeout ou annulation.
func waitAssociation(since time.Time, timeout time.Duration, cancel <-chan struct{}) (lastClient, pairResult) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		if c, ok := readLastClient(); ok && !c.At.Before(since) {
			return c, pairDone
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return lastClient{}, pairTimedOut
		case <-cancel:
			return lastClient{}, pairCancelled
		}
	}
}

// durationText : durée lisible pour un message (« 10 min », « 300ms »).
func durationText(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return d.String()
}

// lastClient : dernière requête authentifiée reçue par l'agent (last-client.json).
type lastClient struct {
	At      time.Time `json:"at"`
	Addr    string    `json:"addr"`
	Machine string    `json:"machine,omitempty"`
}

// lastClientPath : fichier où l'agent note sa dernière requête authentifiée.
func lastClientPath() string {
	return filepath.Join(agentDir(), "last-client.json")
}

// writeLastClient : écriture atomique (droits 600).
func writeLastClient(c lastClient) error {
	if err := os.MkdirAll(agentDir(), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp := lastClientPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, lastClientPath())
}

// readLastClient : la dernière requête authentifiée, si le fichier existe.
func readLastClient() (lastClient, bool) {
	var c lastClient
	data, err := os.ReadFile(lastClientPath())
	if err != nil || json.Unmarshal(data, &c) != nil {
		return c, false
	}
	return c, true
}

// serveTempAgent : sert l'agent temporaire de --pair sur ln (sans fichier de
// PID). Retourne l'arrêt : arrêt propre, connexions en cours comprises.
func serveTempAgent(cfg agentConfig, ln net.Listener) (func(), error) {
	ag, tlsConf, err := prepareAgent(cfg, false)
	if err != nil {
		return nil, err
	}
	hs := ag.httpServer()
	done := make(chan struct{})
	go func() {
		_ = hs.Serve(tls.NewListener(ln, tlsConf))
		close(done)
	}()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hs.Shutdown(ctx); err != nil {
			_ = hs.Close()
		}
		<-done
	}, nil
}

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
	cmd := exec.Command(selfPath, "--agent", "run", "log")
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

// agentConfigured : jeton et certificat présents (agent initialisé).
func agentConfigured() bool {
	return loadAgentConfig().Token != "" &&
		isFile(filepath.Join(agentDir(), "cert.pem")) && isFile(filepath.Join(agentDir(), "key.pem"))
}

// agentStartWait : délai laissé à l'agent lancé pour écouter. Variable pour les tests.
var agentStartWait = 3 * time.Second

// cmdAgentStart : --agent. Lance l'agent en arrière-plan et rend la main. Sans
// configuration, l'initialise comme --pair (jeton, certificat).
func cmdAgentStart() int {
	if !agentConfigured() {
		if _, err := agentInit(nil, 0); err != nil {
			uiError("agent non initialisé : " + err.Error())
			return 1
		}
		uiStatusLine(stOK, "Agent initialisé (jeton, certificat dans "+agentDir()+")")
		uiNote("Code d'association : wsquashfs-launcher --pair")
	}
	cfg := loadAgentConfig()
	if agentListening(cfg.Port) {
		msg := fmt.Sprintf("Agent : déjà en marche sur le port %d", cfg.Port)
		if pid, ok := runningAgentPid(); ok {
			msg += fmt.Sprintf(" (pid %d)", pid)
		}
		uiStatusLine(stSkip, msg)
		return 0
	}
	before := agentLogSize()
	pid, err := startAgentDetached()
	if err != nil {
		uiStatusLine(stFail, "Agent : lancement impossible : "+err.Error())
		return 1
	}
	if !waitAgentUp(cfg.Port, pid, agentStartWait) {
		uiStatusLine(stFail, fmt.Sprintf("Agent (pid %d) : pas en écoute sur le port %d après %s, fin du journal :",
			pid, cfg.Port, durationText(agentStartWait)))
		tail := agentLogSince(before)
		if len(tail) == 0 {
			tail = []string{"(journal vide)"}
		}
		uiNote(tail...)
		return 1
	}
	uiStatusLine(stOK, fmt.Sprintf("Agent lancé (pid %d, port %d), journal : %s", pid, cfg.Port, agentLogPath()))
	uiNote("Arrêter : wsquashfs-launcher --agent stop")
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
		uiStatusLine(stSkip, "Agent : non configuré (« wsquashfs-launcher --agent » l'initialise)")
		return 0
	}
	cfg := loadAgentConfig()
	if pid, ok := runningAgentPid(); ok {
		uiStatusLine(stOK, fmt.Sprintf("Agent en marche (pid %d, port %d)", pid, cfg.Port))
	} else if agentListening(cfg.Port) {
		uiStatusLine(stInfo, fmt.Sprintf("Agent : port %d en écoute sans processus : socket systemd en attente (ou --pair en cours)", cfg.Port))
	} else {
		uiStatusLine(stSkip, fmt.Sprintf("Agent : non lancé (port %d)", cfg.Port))
	}
	uiKV(0, kv{"Journal", agentLogPath()})
	return 0
}

// agentPidFile : PID de l'agent en marche.
func agentPidFile() string {
	return filepath.Join(agentDir(), "agent.pid")
}

// cmdAgentStop : --agent stop. Arrête l'agent (SIGTERM) pour le PID inscrit
// dans agentPidFile, seulement si ce processus est bien un agent (sinon, le
// fichier est périmé et retiré). Rien à arrêter : code 0.
func cmdAgentStop() int {
	data, err := os.ReadFile(agentPidFile())
	if err != nil {
		uiStatusLine(stSkip, "Agent : non lancé")
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid <= 0 || !isAgentProcess(pid) {
		_ = os.Remove(agentPidFile())
		uiStatusLine(stSkip, "Agent : non lancé (fichier de PID périmé, retiré)")
		return 0
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		uiStatusLine(stFail, fmt.Sprintf("Agent (pid %d) : arrêt impossible (%v)", pid, err))
		return 1
	}
	for i := 0; i < 50; i++ {
		if syscall.Kill(pid, 0) != nil {
			_ = os.Remove(agentPidFile())
			uiStatusLine(stOK, fmt.Sprintf("Agent arrêté (pid %d)", pid))
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	uiStatusLine(stFail, fmt.Sprintf("Agent (pid %d) toujours en cours après 5 s", pid))
	return 1
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

// agentOwner : utilisateur de la session (pour l'information de l'agent).
func agentOwner() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

// prepareAgent : routes et TLS de l'agent, sans écoute. Erreur : agent non
// initialisé (jeton ou certificat absent) ou certificat illisible.
func prepareAgent(cfg agentConfig, logOn bool) (*agentServer, *tls.Config, error) {
	if cfg.Token == "" || !isFile(filepath.Join(agentDir(), "cert.pem")) {
		return nil, nil, errors.New("Agent non initialisé : « wsquashfs-launcher --agent » d'abord")
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(agentDir(), "cert.pem"), filepath.Join(agentDir(), "key.pem"))
	if err != nil {
		return nil, nil, errors.New("certificat illisible : " + err.Error())
	}
	tlsConf := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	return newAgentServer(cfg, logOn), tlsConf, nil
}

// inheritedSocket : systemd a passé une socket d'écoute à ce processus.
func inheritedSocket() bool {
	return os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) && os.Getenv("LISTEN_FDS") != ""
}

// agentListener : socket donnée par systemd si ce processus l'a reçue, sinon le
// port configuré. socketActivated : vrai pour la socket.
func agentListener(port int) (ln net.Listener, socketActivated bool, err error) {
	if inheritedSocket() {
		// FileListener duplique le descripteur (close-on-exec) ; le 3 d'origine
		// est fermé, sinon Wine et les jeux lancés par l'agent en hériteraient.
		f := os.NewFile(3, "systemd-socket")
		ln, err = net.FileListener(f)
		f.Close()
		if err == nil {
			return ln, true, nil
		}
	}
	ln, err = net.Listen("tcp", fmt.Sprintf(":%d", port))
	return ln, false, err
}

// serveAgent : premier plan (--agent run, socket systemd). Écoute TLS, PID
// pour « --agent stop ». logOn : trace des requêtes sur la sortie d'erreur.
func serveAgent(logOn bool) int {
	cfg := loadAgentConfig()
	ag, tlsConf, err := prepareAgent(cfg, logOn)
	if err != nil {
		uiError(err.Error())
		return 1
	}
	ln, socketActivated, err := agentListener(cfg.Port)
	if err != nil {
		uiError("écoute impossible : " + err.Error())
		return 1
	}
	if !socketActivated {
		uiErr(stOK.mark() + fmt.Sprintf(" wsquashfs-agent en écoute sur le port %d", cfg.Port))
	}
	httpSrv := ag.httpServer()
	if socketActivated {
		go ag.idleWatchdog(httpSrv)
	}
	// Pour « --agent stop » : le PID de cet agent (retiré à l'arrêt propre).
	if err := os.WriteFile(agentPidFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err == nil {
		defer os.Remove(agentPidFile())
	}
	if err := httpSrv.Serve(tls.NewListener(ln, tlsConf)); err != nil && err != http.ErrServerClosed {
		uiError("arrêt : " + err.Error())
		return 1
	}
	return 0
}

// agentCmd : sous-commande de --agent.
type agentCmd struct {
	mode  string // "start", "run", "stop", "status"
	logOn bool   // run : trace des requêtes
}

// parseAgentArgs : --agent [run [log] | stop | status] ; sans argument, lancement.
func parseAgentArgs(args []string) (agentCmd, error) {
	if len(args) == 0 {
		return agentCmd{mode: "start"}, nil
	}
	switch args[0] {
	case "stop", "status":
		if len(args) > 1 {
			return agentCmd{}, &argError{msg: "--agent " + args[0] + " ne prend pas d'argument"}
		}
		return agentCmd{mode: args[0]}, nil
	case "run":
		c := agentCmd{mode: "run"}
		for _, a := range args[1:] {
			if a != "log" {
				return agentCmd{}, &argError{msg: "option inconnue pour --agent run : " + a + " (--agent run [log])"}
			}
			c.logOn = true
		}
		return c, nil
	case "log":
		return agentCmd{}, &argError{msg: "« --agent log » seul n'existe plus : utiliser « --agent run log » (premier plan, avec trace)"}
	}
	return agentCmd{}, &argError{msg: "option inconnue pour --agent : " + args[0] + " (--agent [run [log] | stop | status])"}
}

// cmdAgentMain : --agent [run [log] | stop | status]. Sans argument, l'agent
// se lance en arrière-plan et la commande rend la main ; une socket systemd
// héritée (unités d'avant « --agent run ») sert au premier plan.
func cmdAgentMain(args []string) int {
	if len(args) == 0 && inheritedSocket() {
		return serveAgent(false)
	}
	c, err := parseAgentArgs(args)
	if err != nil {
		uiError(err.Error())
		return 1
	}
	switch c.mode {
	case "stop":
		return cmdAgentStop()
	case "status":
		return cmdAgentStatus()
	case "run":
		return serveAgent(c.logOn)
	}
	return cmdAgentStart()
}
