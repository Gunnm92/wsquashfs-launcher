package main

// Route POST /v1/pair : association d'une machine cliente à l'agent (code à usage unique).

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

// pairDetail : refus de POST /v1/pair (code absent, expiré, déjà utilisé ou faux).
const pairDetail = "code d'association expiré ou déjà utilisé : relancer --pair"

// pairRequest : corps de POST /v1/pair.
type pairRequest struct {
	Name string `json:"name"`
}

// pairRoute : POST /v1/pair, hors du contrôle de jeton. Le secret du code (Authorization « Bearer »)
// est consommé à l'usage : pair.json est retiré avant qu'un jeton durable ne soit délivré. Un
// mauvais secret ne consomme pas le code.
func (s *agentServer) pairRoute(w http.ResponseWriter, r *http.Request) {
	machine := r.Header.Get("X-Wsq-Machine")
	if !validSyncID(machine) {
		writeDetail(w, http.StatusBadRequest, "identifiant de machine absent ou invalide")
		return
	}
	var req pairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeDetail(w, http.StatusRequestEntityTooLarge, "requête trop volumineuse")
			return
		}
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	offer, ok := readPairOffer()
	if !ok {
		writeDetail(w, http.StatusUnauthorized, pairDetail)
		return
	}
	if !time.Now().Before(offer.Expires) {
		_ = removePairOffer()
		writeDetail(w, http.StatusUnauthorized, pairDetail)
		return
	}
	secret, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	sum := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(offer.SecretSHA256)) != 1 {
		writeDetail(w, http.StatusUnauthorized, pairDetail)
		return
	}
	// Usage unique : retiré avant tout jeton délivré. Déjà retiré entre-temps : refusé.
	if err := removePairOffer(); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeDetail(w, http.StatusUnauthorized, pairDetail)
			return
		}
		writeDetail(w, http.StatusInternalServerError, "code d'association non retiré, rien délivré")
		return
	}
	token, err := newToken()
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "erreur interne de l'agent")
		return
	}
	tokSum := sha256.Sum256([]byte(token))
	addr := clientAddr(r)
	name := cleanMachineName(req.Name)
	if name == "" {
		name = addr
	}
	now := time.Now()
	cfg := loadAgentConfig()
	previous := cfg.Machines
	cfg.Machines = upsertMachine(previous, agentMachine{
		ID:          machine,
		Name:        name,
		TokenSHA256: hex.EncodeToString(tokSum[:]),
		Paired:      now,
	})
	if err := saveAgentConfig(cfg); err != nil {
		writeDetail(w, http.StatusInternalServerError, "association non enregistrée : "+err.Error())
		return
	}
	if err := writeJSONAtomic600(lastPairPath(), lastPair{At: now, Addr: addr, ID: machine, Name: name}); err != nil {
		// Sans cette trace, --pair ne verrait pas l'association : elle est annulée.
		cfg.Machines = previous
		_ = saveAgentConfig(cfg)
		writeDetail(w, http.StatusInternalServerError, "association non enregistrée : "+err.Error())
		return
	}
	host, _ := os.Hostname()
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "host": host, "version": agentVersion})
}

// clientAddr : adresse du client (sans le port).
func clientAddr(r *http.Request) string {
	addr, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return addr
}

// upsertMachine : remplace l'entrée de même identifiant (sa place est gardée), ou ajoute la machine.
// Retourne une nouvelle liste : celle reçue n'est pas modifiée.
func upsertMachine(machines []agentMachine, m agentMachine) []agentMachine {
	out := make([]agentMachine, 0, len(machines)+1)
	replaced := false
	for _, old := range machines {
		if old.ID == m.ID {
			out = append(out, m)
			replaced = true
			continue
		}
		out = append(out, old)
	}
	if !replaced {
		out = append(out, m)
	}
	return out
}

// cleanMachineName : nom d'une machine tel qu'il est gardé : caractères de contrôle et espaces de
// bord retirés, 64 caractères au plus.
func cleanMachineName(name string) string {
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name))
	if runes := []rune(name); len(runes) > 64 {
		name = strings.TrimSpace(string(runes[:64]))
	}
	return name
}
