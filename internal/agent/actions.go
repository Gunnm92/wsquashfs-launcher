package agent

// Routes POST /v1/trial et POST /v1/maintenance : actions du lanceur lancées en sous-processus.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// trialStopDelay : temps laissé à --trial interrompu pour arrêter le jeu
// (5 s pour les processus, 30 s pour le démontage, marge).
const trialStopDelay = 60 * time.Second

type trialRequest struct {
	Rel      string `json:"rel"`
	Duration int    `json:"duration"`
}

// trialRoute : essai chronométré d'une image des dossiers roms/.
func (s *service) trialRoute(w http.ResponseWriter, r *http.Request) {
	var req trialRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req); err != nil {
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	if req.Duration == 0 {
		req.Duration = 60
	}
	if req.Duration < 10 || req.Duration > 600 {
		writeDetail(w, http.StatusBadRequest, "durée hors limites (10 à 600 s)")
		return
	}
	image, code, msg := s.resolveImage(req.Rel)
	if code != 0 {
		writeDetail(w, code, msg)
		return
	}
	if !s.trial.TryLock() {
		writeDetail(w, http.StatusConflict, "un essai est déjà en cours sur cette machine")
		return
	}
	defer s.trial.Unlock()

	shot, err := os.CreateTemp("", "wsfs-shot.*.png")
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "erreur interne de l'agent")
		return
	}
	shot.Close()
	defer os.Remove(shot.Name())

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.Duration+180)*time.Second)
	defer cancel()
	args := []string{"--trial", image, fmt.Sprint(req.Duration), "session", s.cfg.Session, "screenshot", shot.Name()}
	cmd := exec.CommandContext(ctx, s.env.Exe(), args...)
	// Interruption (délai, client parti) : SIGTERM, pour que --trial arrête
	// le jeu et démonte ; SIGKILL seulement s'il ne s'est pas arrêté à temps.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = trialStopDelay
	out, err := cmd.Output()
	if ctx.Err() != nil {
		writeDetail(w, http.StatusGatewayTimeout, fmt.Sprintf("essai trop long (%d s)", req.Duration))
		return
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			switch ee.ExitCode() {
			case 3:
				writeDetail(w, http.StatusConflict, "un jeu tourne déjà sur cette machine")
				return
			case 4:
				writeDetail(w, http.StatusNotFound, "image introuvable")
				return
			}
		}
	}
	var report json.RawMessage
	if json.Unmarshal(out, &report) != nil {
		writeDetail(w, http.StatusInternalServerError, "rapport d'essai illisible")
		return
	}
	var screenshot any
	if png, err := os.ReadFile(shot.Name()); err == nil && len(png) > 8 {
		screenshot = base64.StdEncoding.EncodeToString(png)
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": report, "screenshot": screenshot})
}

// gameRule : la place du jeu (rel) dans une action de maintenance.
type gameRule int

const (
	noGame       gameRule = iota // rel interdit
	gameRequired                 // rel obligatoire (son image devient l'argument)
	gameOptional                 // rel facultatif
)

// maintenanceSpec : arguments du lanceur pour une action de l'agent (sans le jeu) et règle sur le jeu.
type maintenanceSpec struct {
	args []string
	game gameRule
}

// maintenanceActions : liste fermée, action de l'agent → lanceur (pas de --clear, --erase, --pack,
// --install). Le contrat est celui de wsquashfs-manager ; la version de l'agent (agentVersion) le suit.
var maintenanceActions = map[string]maintenanceSpec{
	"list":        {[]string{"--list"}, noGame},
	"umount":      {[]string{"--umount"}, noGame},
	"clean":       {[]string{"--clean"}, noGame},
	"save":        {[]string{"--save"}, gameRequired},
	"saves":       {[]string{"--saves"}, noGame},
	"sync":        {[]string{"--sync"}, gameOptional},
	"sync-status": {[]string{"--sync", "status"}, noGame},
}

// maintenanceStopDelay : temps laissé à une action de maintenance interrompue pour s'arrêter
// proprement (démontage : quelques secondes). Variable pour les tests.
var maintenanceStopDelay = 30 * time.Second

type maintenanceRequest struct {
	Action string `json:"action"`
	Rel    string `json:"rel"`
}

// maintenance : une action de maintenance du lanceur, selon la table maintenanceActions.
func (s *service) maintenance(w http.ResponseWriter, r *http.Request) {
	var req maintenanceRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req); err != nil {
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	spec, ok := maintenanceActions[req.Action]
	if !ok {
		writeDetail(w, http.StatusBadRequest, "action de maintenance non autorisée")
		return
	}
	args := append([]string{}, spec.args...)
	switch {
	case spec.game == noGame && req.Rel != "":
		writeDetail(w, http.StatusBadRequest, req.Action+" ne prend pas de jeu")
		return
	case spec.game == gameRequired && req.Rel == "":
		writeDetail(w, http.StatusBadRequest, "--save attend un jeu")
		return
	case req.Rel != "":
		image, code, msg := s.resolveImage(req.Rel)
		if code != 0 {
			writeDetail(w, code, msg)
			return
		}
		args = append(args, image)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 900*time.Second)
	defer cancel()
	// Couleurs forcées : wsquashfs-manager affiche la sortie en couleurs
	// (codes ANSI convertis en HTML). NO_COLOR, s'il est posé, l'emporte.
	cmd := exec.CommandContext(ctx, s.env.Exe(), args...)
	cmd.Env = append(os.Environ(), "CLICOLOR_FORCE=1")
	// Interruption (délai, client parti) : SIGTERM, pour que le lanceur fasse son nettoyage (image
	// démontée…) ; SIGKILL seulement s'il ne s'est pas arrêté à temps.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = maintenanceStopDelay
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		out = append(out, []byte("maintenance interrompue (délai dépassé ou client parti)\n")...)
	}
	// code : code de sortie du lanceur, comme en mode local (wsquashfs-manager teste code !== 0).
	// -1 si le processus n'a pas pu démarrer ou a été tué (délai) : le motif est ajouté à la sortie.
	code := 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() >= 0 {
			code = ee.ExitCode()
		} else {
			out = append(out, []byte(err.Error()+"\n")...)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": req.Action, "code": code, "output": string(out)})
}
