package agent

// Activité de la machine pour wsquashfs-manager (agent 11) : jeux en cours, manettes branchées et
// version du lanceur, rendus par GET /v1/info (le manager l'appelle déjà à chaque relecture : aucune
// requête de plus) ; mise à jour du lanceur à distance (POST /v1/update). Rien n'est scruté en
// continu : chaque valeur est lue au moment de la requête.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Playing : un jeu en cours (session écrite par le lancement, dont le jeu tourne encore).
type Playing struct {
	Game  string `json:"game"`  // nom du jeu (image sans .wsquashfs)
	Since int64  `json:"since"` // début de la partie, secondes Unix
}

// Controller : une manette physique branchée.
type Controller struct {
	Kind string `json:"kind"` // dualsense, playstation, xbox, steam, nintendo, gamepad
	Name string `json:"name"` // nom donné par le pilote
	Bus  string `json:"bus"`  // usb, bluetooth, "" (autre)
}

// updateTimeout : durée maximale de --update (téléchargement compris). Variable pour les tests.
var updateTimeout = 5 * time.Minute

// restartDelay : délai entre la réponse de POST /v1/update et la relance de l'agent, le temps que
// la réponse parte. Variable pour les tests.
var restartDelay = 500 * time.Millisecond

func (s *service) build() string {
	if s.env.Build == nil {
		return ""
	}
	return s.env.Build()
}

func (s *service) playing() []Playing {
	if s.env.Playing == nil {
		return []Playing{}
	}
	if p := s.env.Playing(); p != nil {
		return p
	}
	return []Playing{}
}

func (s *service) controllers() []Controller {
	if s.env.Controllers == nil {
		return []Controller{}
	}
	if c := s.env.Controllers(); c != nil {
		return c
	}
	return []Controller{}
}

// update : POST /v1/update. Lance « --update » (binaire publié, vérifié par son empreinte SHA-256 et
// son en-tête ELF : rien n'est remplacé sinon), puis, si la version a changé, relance l'agent sur le
// nouveau binaire une fois la réponse partie. Refusé pendant un envoi d'image (409) et pendant une
// autre mise à jour (409). Réponse : {code, output, before, after, restart}.
func (s *service) update(w http.ResponseWriter, r *http.Request) {
	if s.env.Exe == nil || s.env.Build == nil {
		writeDetail(w, http.StatusNotImplemented, "mise à jour indisponible")
		return
	}
	if !s.updating.TryLock() {
		writeDetail(w, http.StatusConflict, "mise à jour déjà en cours")
		return
	}
	defer s.updating.Unlock()
	s.gamesMu.Lock()
	sending := len(s.uploads) > 0
	s.gamesMu.Unlock()
	if sending {
		writeDetail(w, http.StatusConflict, "envoi d'image en cours : réessayer après")
		return
	}
	before := s.build()
	ctx, cancel := context.WithTimeout(r.Context(), updateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.env.Exe(), "--update")
	cmd.Env = append(os.Environ(), "CLICOLOR_FORCE=1")
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
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
	after := s.build()
	restart := code == 0 && after != "" && after != before && s.env.Restart != nil
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "output": string(out),
		"before": before, "after": after, "restart": restart})
	if !restart {
		return
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	// Délai lu ici, pas dans la goroutine : un test le remet à sa valeur pendant qu'elle attend.
	restartFn, delay := s.env.Restart, restartDelay
	go func() {
		time.Sleep(delay)
		if err := restartFn(); err != nil {
			// La relance a échoué : l'agent continue sur l'ancien binaire (le nouveau servira au
			// prochain démarrage).
			os.Stderr.WriteString("relance de l'agent impossible : " + err.Error() + "\n")
		}
	}()
}
