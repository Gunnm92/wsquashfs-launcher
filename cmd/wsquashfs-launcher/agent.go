package main

// Agent pour wsquashfs-manager : serveur HTTPS (jeton, certificat épinglé)
// qui expose les opérations du lanceur sur la machine de jeu. Pas
// d'exécution de commande fournie par le client : chaque route correspond à
// une action fixe du lanceur, lancée en sous-processus.
//
// Configuration : ~/.config/wsquashfs/agent/ (agent.json : machines associées ; pair.json : code
// en attente ; last-pair.json ; cert.pem, key.pem).
//
// Commandes :
//   --agent            lance l'agent en arrière-plan et rend la main (le
//                      configure d'abord, comme --pair, si besoin)
//   --agent run [log]  premier plan : systemd (ExecStart) et débogage
//   --agent stop       arrête l'agent lancé (fichier de PID)
//   --agent status     en marche ou non, pid, port, journal
//   --pair             affiche le code wsq://… (à usage unique, délai court),
//                      attend l'association (POST /v1/pair) et se ferme ; sans
//                      agent en marche, sert un agent temporaire le temps de l'attente
//   --pair forget <m>  retire une machine associée (nom ou identifiant)
//   --pair reset       retire toutes les machines associées, puis --pair
//
// La commande --pair est dans pair_cmd.go ; le lancement, l'arrêt et l'état de l'agent,
// ainsi que le service, sont dans agent_run.go.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	agentVersion     = "10"
	agentDefaultPort = 8790
)

// agentConfig : agent.json. Pas de jeton ici : une entrée par machine associée, avec seulement le
// SHA-256 de son jeton durable.
type agentConfig struct {
	Port     int            `json:"port"`
	Machines []agentMachine `json:"machines"`
	Roms     []string       `json:"roms"`
	Session  string         `json:"session_process"`
	GamesDir string         `json:"games_dir,omitempty"` // dossier d'installation choisi (vide : détection, games_http.go)
}

// agentMachine : une machine associée (entrée de agent.json).
type agentMachine struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	TokenSHA256 string    `json:"token_sha256"`
	Paired      time.Time `json:"paired"`
}

// agentDir : dossier de configuration de l'agent.
func agentDir() string {
	base := configHome()
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
	if cfg.Machines == nil {
		cfg.Machines = []agentMachine{}
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

// agentInit : configuration et certificat (créés une fois ; roms et port de --pair remplacent les
// valeurs existantes). Les machines associées sont conservées.
func agentInit(roms []string, port int) (agentConfig, error) {
	cfg := loadAgentConfig()
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

// interruptHook : appelé à la place de la sortie quand un signal arrive
// (installSignals), tant que --pair attend l'association ou que l'agent sert
// (serveAgent : arrêt propre).
var interruptHook atomic.Pointer[func()]

// durationText : durée lisible pour un message (« 10 min », « 300ms »).
func durationText(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return d.String()
}

// writeJSONAtomic600 : écriture atomique (droits 600) d'un fichier JSON du dossier de l'agent.
func writeJSONAtomic600(path string, v any) error {
	if err := os.MkdirAll(agentDir(), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// agentConfigured : certificat et clé présents (agent initialisé). Pas de jeton : il n'existe que
// par machine associée (agent.json).
func agentConfigured() bool {
	return isFile(filepath.Join(agentDir(), "cert.pem")) && isFile(filepath.Join(agentDir(), "key.pem"))
}

// agentOwner : utilisateur de la session (pour l'information de l'agent).
func agentOwner() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
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
// se lance en arrière-plan et la commande rend la main.
func cmdAgentMain(args []string) int {
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
