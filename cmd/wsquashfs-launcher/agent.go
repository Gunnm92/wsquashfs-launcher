package main

// Agent pour wsquashfs-manager : serveur HTTPS (jeton, certificat épinglé)
// qui expose les opérations du lanceur sur la machine de jeu. Pas
// d'exécution de commande fournie par le client : chaque route correspond à
// une action fixe du lanceur, lancée en sous-processus.
//
// Configuration : ~/.config/wsquashfs/agent/ (agent.json, cert.pem, key.pem).
// Association : « wsquashfs-launcher --pair » affiche un code wsq://…, à coller
// dans wsquashfs-manager.

import (
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
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	agentVersion     = "2"
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
// puis affiche le code d'association.
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
	fp, err := certFingerprint()
	if err != nil {
		die("certificat illisible : " + err.Error())
	}
	host := "localhost"
	if addrs := localAddresses(); len(addrs) > 0 {
		host = addrs[0]
	} else if h, err := os.Hostname(); err == nil {
		host = h
	}
	fmt.Println("Code d'association (à coller dans wsquashfs-manager, Réglages → Machine de jeu) :")
	fmt.Println()
	fmt.Println("  " + pairCode(cfg, host, fp))
	fmt.Println()
	fmt.Println("Autres adresses : " + strings.Join(localAddresses(), ", "))
	fmt.Println("Dossiers roms/  : " + strings.Join(cfg.Roms, ", "))
	// Le manager doit pouvoir joindre l'agent tout de suite : --pair le lance
	// s'il ne tourne pas (socket systemd : elle accepte déjà la connexion).
	if agentListening(cfg.Port) {
		fmt.Printf("Agent           : déjà en marche sur le port %d\n", cfg.Port)
	} else if pid, err := startAgentDetached(); err != nil {
		fmt.Println("Agent           : lancement impossible : " + err.Error() + " (lancer « wsquashfs-launcher --agent »)")
	} else {
		fmt.Printf("Agent           : lancé (pid %d, port %d), journal : %s\n", pid, cfg.Port, agentLogPath())
		fmt.Println("                  redémarrage du PC : « --setup --agent » l'installe avec systemd.")
	}
	return 0
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

// agentLogPath : journal de l'agent lancé par --pair.
func agentLogPath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	return filepath.Join(base, "wsquashfs", "agent.log")
}

// startAgentDetached : lance « wsquashfs-launcher --agent » hors du terminal
// (nouvelle session, journal dans agentLogPath). Retourne son PID.
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
	cmd := exec.Command(selfPath, "--agent", "log")
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	time.Sleep(500 * time.Millisecond)
	return pid, nil
}

// agentOwner : utilisateur de la session (pour l'information de l'agent).
func agentOwner() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

// serveAgent : écoute TLS (socket systemd si fournie, sinon le port configuré).
// logOn : trace des requêtes sur la sortie d'erreur (méthode, route, code, durée).
func serveAgent(logOn bool) int {
	cfg := loadAgentConfig()
	if cfg.Token == "" || !isFile(filepath.Join(agentDir(), "cert.pem")) {
		fmt.Fprintln(os.Stderr, "Agent non initialisé : « wsquashfs-launcher --pair » d'abord")
		return 1
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(agentDir(), "cert.pem"), filepath.Join(agentDir(), "key.pem"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "certificat illisible : "+err.Error())
		return 1
	}
	tlsConf := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}

	srv := newAgentServer(cfg, logOn)
	var ln net.Listener
	socketActivated := false
	if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) && os.Getenv("LISTEN_FDS") != "" {
		// FileListener duplique le descripteur (close-on-exec) ; le 3 d'origine
		// est fermé, sinon Wine et les jeux lancés par l'agent en hériteraient.
		f := os.NewFile(3, "systemd-socket")
		ln, err = net.FileListener(f)
		f.Close()
		socketActivated = err == nil
	}
	if ln == nil {
		ln, err = net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
		if err != nil {
			fmt.Fprintln(os.Stderr, "écoute impossible : "+err.Error())
			return 1
		}
		fmt.Fprintf(os.Stderr, "wsquashfs-agent en écoute sur le port %d\n", cfg.Port)
	}
	httpSrv := srv.httpServer()
	if socketActivated {
		go srv.idleWatchdog(httpSrv)
	}
	if err := httpSrv.Serve(tls.NewListener(ln, tlsConf)); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "arrêt : "+err.Error())
		return 1
	}
	return 0
}

// cmdAgentMain : --agent [log]. Sert les requêtes ; « log » trace chacune
// (méthode, chemin, code, durée) sur la sortie d'erreur.
func cmdAgentMain(args []string) int {
	logOn := false
	for _, a := range args {
		switch a {
		case "log":
			logOn = true
		default:
			die("option inconnue pour --agent : " + a + " (--agent [log])")
		}
	}
	return serveAgent(logOn)
}
