package main

// Commande --pair : code d'association (wsq://…), attente de l'association,
// machines associées (--pair forget, --pair reset) et agent temporaire le temps de l'attente.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"wsquashfs-launcher/internal/ui"
)

// pairCode : code d'association wsq://SECRET@HÔTE:PORT#EMPREINTE. Le secret est celui du code
// en attente (pair.json) : il ne sert qu'une fois.
func pairCode(secret, host string, port int, fp string) string {
	return fmt.Sprintf("wsq://%s@%s:%d#%s", secret, host, port, fp)
}

// cmdPair : --pair [reset] [roms DIR]... [port N], ou --pair forget <machine>. Initialise si besoin,
// affiche le code d'association à usage unique, puis attend l'association (pairRun).
func cmdPair(args []string) int {
	if len(args) > 0 && args[0] == "forget" {
		if len(args) != 2 {
			usageError("wsquashfs-launcher --pair forget <machine>")
		}
		return cmdPairForget(args[1])
	}
	var roms []string
	port := 0
	reset := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "reset":
			reset = true
		case "roms":
			dir := argAt(args, i+1)
			if dir == "" {
				die("--pair roms attend un dossier")
			}
			roms = append(roms, dir)
			i++
		case "port":
			n, err := strconv.Atoi(argAt(args, i+1))
			if err != nil || n < 1 || n > 65535 {
				die("--pair port attend un numéro de port (1 à 65535)")
			}
			port = n
			i++
		default:
			die("option inconnue pour --pair : " + args[i] + " (reset, roms DIR, port N, forget <machine>)")
		}
	}
	if reset {
		names, err := pairRevokeAll()
		if err != nil {
			die("configuration impossible : " + err.Error())
		}
		if len(names) == 0 {
			ui.StatusLine(ui.Skip, "Reset : aucune machine associée à retirer")
		} else {
			ui.StatusLine(ui.OK, "Reset : toutes les machines associées sont retirées ("+strings.Join(names, ", ")+")")
		}
	}
	cfg, err := agentInit(roms, port)
	if err != nil {
		die("agent non initialisé : " + err.Error())
	}
	secret, err := pairSecretNew()
	if err != nil {
		die("code d'association impossible : " + err.Error())
	}
	code, err := currentPairCode(cfg, secret)
	if err != nil {
		die("certificat illisible : " + err.Error())
	}
	// Agent : celui qui tourne déjà (systemd, --agent), sinon un agent temporaire servi par ce
	// processus, arrêté après l'association. Le port est pris avant que le code ne soit écrit.
	var ln net.Listener
	state := fmt.Sprintf("déjà en marche sur le port %d", cfg.Port)
	if !agentListening(cfg.Port) {
		ln, err = net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
		if err != nil {
			die(fmt.Sprintf("agent temporaire impossible sur le port %d : %s", cfg.Port, err.Error()))
		}
		state = fmt.Sprintf("temporaire (port %d), arrêté après l'association", cfg.Port)
	}
	if err := beginPairing(secret, pairTimeout); err != nil {
		die("code d'association impossible : " + err.Error())
	}
	ui.Title("Association avec wsquashfs-manager")
	ui.Note("Colle ce code sur l'autre machine (wsquashfs-launcher --sync wsq://…)",
		"ou dans wsquashfs-manager (Réglages → Machine de jeu, ou onglet Sauvegardes) :")
	ui.Out("")
	ui.Code(code)
	ui.Out("")
	ui.Note("Usage unique : ce code ne sert qu'une fois, et seulement pendant " + durationText(pairTimeout) + ".")
	ui.KV(0, ui.Pair{"Autres adresses", strings.Join(localAddresses(), ", ")}, ui.Pair{"Dossiers roms/", strings.Join(cfg.Roms, ", ")}, ui.Pair{"Agent", state})
	// Ctrl-C : annulation propre (installSignals appelle ce crochet, sans sortir).
	cancel := make(chan struct{})
	var once sync.Once
	hook := func() { once.Do(func() { close(cancel) }) }
	interruptHook.Store(&hook)
	defer interruptHook.Store(nil)
	return pairRun(cfg, ln, pairTimeout, cancel)
}

// pairSecretNew : secret d'un code d'association neuf. Variable : les tests fixent le secret.
var pairSecretNew = newToken

// pairRevokeAll : retire toutes les machines associées (--pair reset). Retourne leurs noms.
func pairRevokeAll() ([]string, error) {
	cfg := loadAgentConfig()
	var names []string
	for _, m := range cfg.Machines {
		names = append(names, m.Name)
	}
	cfg.Machines = nil
	return names, saveAgentConfig(cfg)
}

// cmdPairForget : --pair forget <machine> : retire les machines de ce nom (sans tenir compte de la
// casse) ou de cet identifiant. Pas d'association : l'agent relit agent.json à chaque requête, la
// révocation prend effet tout de suite, agent en marche ou non.
func cmdPairForget(name string) int {
	cfg := loadAgentConfig()
	var kept []agentMachine
	var gone []string
	for _, m := range cfg.Machines {
		if strings.EqualFold(m.Name, name) || m.ID == name {
			gone = append(gone, m.Name)
			continue
		}
		kept = append(kept, m)
	}
	if len(gone) == 0 {
		ui.Error("aucune machine associée nommée ou d'identifiant « " + name + " » (machines : " + agentMachineNames(cfg) + ")")
		return 1
	}
	cfg.Machines = kept
	if err := saveAgentConfig(cfg); err != nil {
		ui.Error("configuration impossible : " + err.Error())
		return 1
	}
	ui.Title("Machines associées")
	ui.StatusLine(ui.OK, strings.Join(gone, ", ")+" retirée")
	return 0
}

// agentMachineNames : noms des machines associées, « aucune » si la liste est vide.
func agentMachineNames(cfg agentConfig) string {
	if len(cfg.Machines) == 0 {
		return "aucune"
	}
	names := make([]string, 0, len(cfg.Machines))
	for _, m := range cfg.Machines {
		names = append(names, m.Name)
	}
	return strings.Join(names, ", ")
}

// currentPairCode : code wsq:// de cette machine pour le secret donné (hôte : première adresse locale).
func currentPairCode(cfg agentConfig, secret string) (string, error) {
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
	return pairCode(secret, host, cfg.Port, fp), nil
}

// pairTimeout : délai d'attente de l'association (--pair). Variable pour les tests.
var pairTimeout = 10 * time.Minute

// pairResult : issue de l'attente d'association.
type pairResult int

const (
	pairDone pairResult = iota
	pairTimedOut
	pairCancelled
)

// pairRun : attend l'association (POST /v1/pair) et rend le code de --pair : 0 associé, 1 délai
// dépassé ou agent temporaire impossible, 130 annulé. Le code en attente (pair.json) est retiré à la
// fin, quelle qu'elle soit. ln : écoute d'un agent temporaire à servir (nil : un agent tourne déjà).
func pairRun(cfg agentConfig, ln net.Listener, timeout time.Duration, cancel <-chan struct{}) int {
	defer func() { _ = removePairOffer() }()
	since := time.Now() // pris avant l'écoute : une association dès le premier instant est vue
	stop := func() {}
	if ln != nil {
		s, err := serveTempAgent(cfg, ln)
		if err != nil {
			_ = ln.Close()
			ui.StatusLine(ui.Fail, "Agent temporaire impossible : "+err.Error())
			return 1
		}
		stop = s
	}
	ui.StatusLine(ui.Info, "En attente de l'association… (Ctrl-C pour annuler)")
	last, res := waitAssociation(since, timeout, cancel)
	stop()
	switch res {
	case pairCancelled:
		ui.StatusLine(ui.Skip, "Association annulée")
		return 130
	case pairTimedOut:
		ui.StatusLine(ui.Fail, "Association : aucune association en "+durationText(timeout))
		return 1
	}
	ui.StatusLine(ui.OK, "Associé : "+last.Name+" ("+last.Addr+")")
	if ln != nil {
		ui.Note("Pour les synchros suivantes, l'agent doit tourner sur cette machine :",
			"wsquashfs-launcher --agent (arrêt : --agent stop)")
	}
	return 0
}

// waitAssociation : attend une association reçue depuis since (last-pair.json, écrit par la route
// POST /v1/pair), jusqu'à timeout ou annulation. Une requête d'une machine déjà associée ne compte pas.
func waitAssociation(since time.Time, timeout time.Duration, cancel <-chan struct{}) (lastPair, pairResult) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		if p, ok := readLastPair(); ok && !p.At.Before(since) {
			return p, pairDone
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return lastPair{}, pairTimedOut
		case <-cancel:
			return lastPair{}, pairCancelled
		}
	}
}

// lastPair : dernière association réussie (last-pair.json), écrite par la route POST /v1/pair.
type lastPair struct {
	At   time.Time `json:"at"`
	Addr string    `json:"addr"`
	ID   string    `json:"id"`
	Name string    `json:"name"`
}

// lastPairPath : fichier de la dernière association réussie.
func lastPairPath() string {
	return filepath.Join(agentDir(), "last-pair.json")
}

// readLastPair : la dernière association, si le fichier existe et se lit.
func readLastPair() (lastPair, bool) {
	var p lastPair
	data, err := os.ReadFile(lastPairPath())
	if err != nil || json.Unmarshal(data, &p) != nil {
		return p, false
	}
	return p, true
}

// pairOffer : code d'association en attente (pair.json). Seule l'empreinte du secret est écrite ;
// le fichier est retiré à l'usage, à l'expiration constatée et à la fin de --pair.
type pairOffer struct {
	SecretSHA256 string    `json:"secret_sha256"`
	Expires      time.Time `json:"expires"`
}

// pairPath : fichier du code d'association en attente.
func pairPath() string {
	return filepath.Join(agentDir(), "pair.json")
}

// beginPairing : écrit le code d'association en attente, valable ttl (à usage unique).
func beginPairing(secret string, ttl time.Duration) error {
	sum := sha256.Sum256([]byte(secret))
	return writeJSONAtomic600(pairPath(), pairOffer{
		SecretSHA256: hex.EncodeToString(sum[:]),
		Expires:      time.Now().Add(ttl),
	})
}

// readPairOffer : le code en attente, s'il existe et se lit.
func readPairOffer() (pairOffer, bool) {
	var o pairOffer
	data, err := os.ReadFile(pairPath())
	if err != nil || json.Unmarshal(data, &o) != nil {
		return o, false
	}
	return o, true
}

// removePairOffer : retire le code en attente (erreur ErrNotExist si déjà retiré).
func removePairOffer() error {
	return os.Remove(pairPath())
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
