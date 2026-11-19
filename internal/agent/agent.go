// Package agent : l'agent HTTPS de wsquashfs-manager (--agent, --pair).
//
// Agent pour wsquashfs-manager : serveur HTTPS (jeton, certificat épinglé)
// qui expose les opérations du lanceur sur la machine de jeu. Pas
// d'exécution de commande fournie par le client : chaque route correspond à
// une action fixe du lanceur, lancée en sous-processus.
//
// Configuration : ~/.config/wsquashfs/agent/ (agent.json : machines associées ; pair.json : code
// en attente ; last-pair.json ; cert.pem, key.pem).
//
// Commandes :
//
//	--agent            lance l'agent en arrière-plan et rend la main (le
//	                   configure d'abord, comme --pair, si besoin)
//	--agent run [log]  premier plan : systemd (ExecStart) et débogage
//	--agent stop       arrête l'agent lancé (fichier de PID)
//	--agent status     en marche ou non, pid, port, journal
//	--pair             affiche le code wsq://… (à usage unique, délai court),
//	                   attend l'association (POST /v1/pair) et se ferme ; sans
//	                   agent en marche, sert un agent temporaire le temps de l'attente
//	--pair forget <m>  retire une machine associée (nom ou identifiant)
//	--pair reset       retire toutes les machines associées, puis --pair
//
// La commande --pair est dans pair.go ; le lancement, l'arrêt et l'état de l'agent,
// ainsi que le service, sont dans run.go. Le paquet n'arrête jamais le processus : les commandes
// (Run, Pair) renvoient le code de sortie. Ce qu'il tient du lanceur (binaire, synchro des
// sauvegardes, runners, signaux) lui est passé par Env.
package agent

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
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"wsquashfs-launcher/internal/savesync"
	"wsquashfs-launcher/internal/ui"
)

const (
	agentVersion     = "11"
	agentDefaultPort = 8790
)

// Env : ce que l'agent reçoit du lanceur. Les champs fonction sont appelés à chaque usage : ils
// suivent l'état du lanceur du moment.
type Env struct {
	Exe          func() string                      // chemin du lanceur, relancé pour les actions (trial, maintenance, doctor) et pour --agent run log
	Syncer       func() *savesync.Syncer            // synchro des sauvegardes (routes /v1/saves/*)
	ListRunners  func() []string                    // runners Wine installés (routes /v1/runner/*)
	LockIdleGame func(name string) (*os.File, bool) // verrou d'un jeu arrêté (faux : il tourne), pour supprimer son image (DELETE /v1/games/file)
	SetInterrupt func(hook func())                  // crochet appelé à la place de la sortie quand un signal arrive (nil : le retire), pendant que --pair attend ou que l'agent sert
	StopGrace    time.Duration                      // délai laissé aux requêtes en cours à l'arrêt (signal) ; 0 : 3 s
	MaxBody      func() int64                       // taille maximale d'une archive de sauvegardes reçue, relue à chaque envoi ; absent ou 0 : savesync.DefaultMaxBody
	// Activité de la machine (GET /v1/info) et mise à jour (POST /v1/update), voir activity.go.
	// Chacun est facultatif : absent, l'information est vide ou la route répond 501.
	Build       func() string       // version du lanceur (début de l'empreinte SHA-256 du binaire)
	Playing     func() []Playing    // jeux en cours sur cette machine
	Controllers func() []Controller // manettes physiques branchées
	Restart     func() error        // relance l'agent sur le binaire du lanceur (après une mise à jour)
}

// Server : l'agent de cette machine, avec son environnement : commandes --agent et --pair,
// configuration, état du processus et routes HTTPS (Handler).
type Server struct {
	env Env
}

// New : agent pour cet environnement.
func New(env Env) *Server {
	return &Server{env: env}
}

// HTTPServer : serveur HTTP de l'agent pour cette configuration (routes, authentification, délais),
// sans écoute ni TLS : ses tests l'appellent en mémoire.
func (s *Server) HTTPServer(cfg Config, logOn bool) *http.Server {
	return s.newService(cfg, logOn).httpServer()
}

// LoadConfig : agent.json (valeurs par défaut pour ce qui manque).
func (s *Server) LoadConfig() Config { return loadConfig() }

// SaveConfig : écrit agent.json.
func (s *Server) SaveConfig(cfg Config) error { return saveConfig(cfg) }

// Init : configuration et certificat de l'agent (créés une fois).
func (s *Server) Init(roms []string, port int) (Config, error) { return agentInit(roms, port) }

// Dir : dossier de configuration de l'agent.
func (s *Server) Dir() string { return agentDir() }

// LogPath : journal de l'agent lancé en arrière-plan (--agent).
func (s *Server) LogPath() string { return agentLogPath() }

// DefaultRoms : premier dossier roms/ usuel qui existe.
func (s *Server) DefaultRoms() string { return agentDefaultRoms() }

// CertFingerprint : SHA-256 du certificat de l'agent, en hexadécimal majuscule.
func (s *Server) CertFingerprint() (string, error) { return certFingerprint() }

// Running : PID de l'agent en marche, s'il l'est.
func (s *Server) Running() (int, bool) { return runningAgentPid() }

// RemoveUnits : retire le démarrage automatique par systemd des anciennes versions. Vrai si retiré.
func (s *Server) RemoveUnits() bool { return removeAgentUnits() }

// Config : agent.json. Pas de jeton ici : une entrée par machine associée, avec seulement le
// SHA-256 de son jeton durable.
type Config struct {
	Port     int       `json:"port"`
	Machines []Machine `json:"machines"`
	Roms     []string  `json:"roms"`
	Session  string    `json:"session_process"`
	GamesDir string    `json:"games_dir,omitempty"` // dossier d'installation choisi (vide : détection, games.go)
}

// Machine : une machine associée (entrée de agent.json).
type Machine struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	TokenSHA256 string    `json:"token_sha256"`
	Paired      time.Time `json:"paired"`
}

// agentDefaultRoms : premier dossier roms/ usuel qui existe.
func agentDefaultRoms() string {
	home := os.Getenv("HOME")
	for _, d := range []string{"/userdata/roms", filepath.Join(home, "roms"), "/config/userdata/roms", filepath.Join(home, "games/Batocera/roms")} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return filepath.Join(home, "roms")
}

// configHome : dossier de configuration de l'utilisateur ($XDG_CONFIG_HOME, sinon ~/.config).
func configHome() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return base
}

// isFile : fichier ordinaire (test -f).
func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// isDirPath : le chemin est un dossier (liens suivis).
func isDirPath(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// agentDir : dossier de configuration de l'agent.
func agentDir() string {
	base := configHome()
	return filepath.Join(base, "wsquashfs", "agent")
}

func loadConfig() Config {
	var cfg Config
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

// saveConfig : écriture atomique, droits 600.
func saveConfig(cfg Config) error {
	if err := os.MkdirAll(agentDir(), 0o700); err != nil {
		return err
	}
	if cfg.Machines == nil {
		cfg.Machines = []Machine{}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encodage de agent.json : %w", err)
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
		return "", fmt.Errorf("génération du jeton : %w", err)
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
		return fmt.Errorf("génération de la clé : %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return fmt.Errorf("numéro de série du certificat : %w", err)
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
		return fmt.Errorf("création du certificat : %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("encodage de la clé : %w", err)
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
		return "", errors.New("certificat illisible")
	}
	sum := sha256.Sum256(block.Bytes)
	return strings.ToUpper(hex.EncodeToString(sum[:])), nil
}

// agentInit : configuration et certificat (créés une fois ; roms et port de --pair remplacent les
// valeurs existantes). Les machines associées sont conservées.
func agentInit(roms []string, port int) (Config, error) {
	cfg := loadConfig()
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
	if err := saveConfig(cfg); err != nil {
		return cfg, err
	}
	return cfg, ensureAgentCert()
}

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
		return fmt.Errorf("encodage de %s : %w", filepath.Base(path), err)
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
			return agentCmd{}, errors.New("--agent " + args[0] + " ne prend pas d'argument")
		}
		return agentCmd{mode: args[0]}, nil
	case "run":
		c := agentCmd{mode: "run"}
		for _, a := range args[1:] {
			if a != "log" {
				return agentCmd{}, errors.New("option inconnue pour --agent run : " + a + " (--agent run [log])")
			}
			c.logOn = true
		}
		return c, nil
	case "log":
		return agentCmd{}, errors.New("« --agent log » seul n'existe plus : utiliser « --agent run log » (premier plan, avec trace)")
	}
	return agentCmd{}, errors.New("option inconnue pour --agent : " + args[0] + " (--agent [run [log] | stop | status])")
}

// Run : --agent [run [log] | stop | status]. Sans argument, l'agent
// se lance en arrière-plan et la commande rend la main. Retourne le code de sortie.
func (s *Server) Run(args []string) int {
	c, err := parseAgentArgs(args)
	if err != nil {
		ui.Error(err.Error())
		return 1
	}
	switch c.mode {
	case "stop":
		return cmdAgentStop()
	case "status":
		return cmdAgentStatus()
	case "run":
		return s.serve(c.logOn)
	}
	return s.start()
}
