package main

// Activité de la machine pour wsquashfs-manager (agent 11) : sessions de jeu (jeux en cours),
// manettes branchées, version du binaire, relance de l'agent après une mise à jour, et événements
// envoyés au manager déclaré (partie lancée, terminée). Sobre sur le réseau : rien n'est scruté,
// deux petites requêtes par partie, seulement si un manager est déclaré.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"wsquashfs-launcher/internal/agent"
	"wsquashfs-launcher/internal/download"
)

// ── Sessions de jeu ─────────────────────────────────────────────────────

// sessionDir : sessions des parties en cours (une par jeu), à côté des verrous.
func sessionDir() string {
	return filepath.Join(conf.cacheDir, "sessions")
}

// writeSession : début de la partie (lu par l'agent). Une session orpheline (arrêt brutal) est
// ignorée : l'agent ne garde que les jeux qui tournent encore (gameRunning).
func writeSession(name string, started time.Time) {
	_ = os.MkdirAll(sessionDir(), 0o755)
	chownToTarget(sessionDir())
	data, _ := json.Marshal(agent.Playing{Game: name, Since: started.Unix()})
	tmp := filepath.Join(sessionDir(), name+".json.tmp")
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, filepath.Join(sessionDir(), name+".json"))
	}
}

// removeSession : fin de la partie.
func removeSession(name string) {
	_ = os.Remove(filepath.Join(sessionDir(), name+".json"))
}

// playingGames : jeux en cours d'après les sessions, seulement s'ils tournent encore.
func playingGames() []agent.Playing {
	files, _ := filepath.Glob(filepath.Join(sessionDir(), "*.json"))
	out := []agent.Playing{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p agent.Playing
		if json.Unmarshal(data, &p) != nil || p.Game == "" || p.Game+".json" != filepath.Base(f) {
			continue
		}
		if gameRunning(p.Game) {
			out = append(out, p)
		}
	}
	return out
}

// ── Manettes branchées ──────────────────────────────────────────────────

// inputDevices : liste des périphériques d'entrée du noyau, surchargeable (tests).
func inputDevices() string {
	return envOr("INPUT_DEVICES", "/proc/bus/input/devices")
}

// controllerKind : famille d'une manette d'après son fabricant (et son modèle pour Sony).
func controllerKind(vendor, product string) string {
	switch strings.ToLower(vendor) {
	case "054c":
		if p := strings.ToLower(product); p == "0ce6" || p == "0df2" {
			return "dualsense"
		}
		return "playstation"
	case "045e":
		return "xbox"
	case "28de":
		return "steam"
	case "057e":
		return "nintendo"
	}
	return "gamepad"
}

// connectedControllers : manettes physiques (gestionnaire js du noyau), sans les manettes virtuelles
// (Steam Input, manettes uhid : /devices/virtual/), une par manette.
func connectedControllers() []agent.Controller {
	f, err := os.Open(inputDevices())
	if err != nil {
		return []agent.Controller{}
	}
	defer f.Close()
	out := []agent.Controller{}
	var bus, vendor, product, name, sysfs, handlers string
	flush := func() {
		if strings.Contains(" "+handlers+" ", " js") && !strings.Contains(sysfs, "/devices/virtual/") {
			b := map[string]string{"0003": "usb", "0005": "bluetooth"}[bus]
			out = append(out, agent.Controller{Kind: controllerKind(vendor, product), Name: name, Bus: b})
		}
		bus, vendor, product, name, sysfs, handlers = "", "", "", "", "", ""
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.TrimSpace(line) == "":
			flush()
		case strings.HasPrefix(line, "I: "):
			for _, field := range strings.Fields(line[3:]) {
				k, v, _ := strings.Cut(field, "=")
				switch k {
				case "Bus":
					bus = v
				case "Vendor":
					vendor = v
				case "Product":
					product = v
				}
			}
		case strings.HasPrefix(line, "N: Name="):
			name = strings.Trim(strings.TrimPrefix(line, "N: Name="), `"`)
		case strings.HasPrefix(line, "S: Sysfs="):
			sysfs = strings.TrimPrefix(line, "S: Sysfs=")
		case strings.HasPrefix(line, "H: Handlers="):
			handlers = strings.TrimPrefix(line, "H: Handlers=")
		}
	}
	flush()
	return out
}

// ── Version du binaire et relance de l'agent ────────────────────────────

var buildCache struct {
	sync.Mutex
	size  int64
	mtime time.Time
	build string
}

// cachedBuild : launcherBuild, recalculé seulement si le binaire a changé (taille, date) : GET
// /v1/info le demande à chaque relecture du manager.
func cachedBuild() string {
	exe := launcherExe()
	st, err := os.Stat(exe)
	if err != nil {
		return "inconnue"
	}
	buildCache.Lock()
	defer buildCache.Unlock()
	if buildCache.build == "" || st.Size() != buildCache.size || !st.ModTime().Equal(buildCache.mtime) {
		sum, err := download.SHA256(exe)
		if err != nil {
			return "inconnue"
		}
		buildCache.size, buildCache.mtime, buildCache.build = st.Size(), st.ModTime(), shortBuild(sum)
	}
	return buildCache.build
}

// restartAgent : remplace l'agent par le binaire du lanceur (mis à jour), mêmes arguments : même
// processus (PID, fichier de PID et service inchangés), nouvelle version.
func restartAgent() error {
	return syscall.Exec(launcherExe(), os.Args, os.Environ())
}

// ── Événements pour le manager ──────────────────────────────────────────

// gameEvent : corps de POST /v1/events du manager.
type gameEvent struct {
	Event       string             `json:"event"` // start, exit
	Game        string             `json:"game"`
	At          int64              `json:"at"`
	Started     int64              `json:"started,omitempty"`
	Code        *int               `json:"code,omitempty"`
	Controllers []agent.Controller `json:"controllers,omitempty"`
}

// eventWait : attente maximale de l'envoi de l'événement de fin de partie (la sortie du lanceur ne
// traîne pas pour un manager lent). Variable pour les tests.
var eventWait = 5 * time.Second

// notifyGameStart : partie lancée, envoyée au manager sans retarder le jeu.
func notifyGameStart(name string, started time.Time) {
	ev := gameEvent{Event: "start", Game: name, At: started.Unix(), Controllers: connectedControllers()}
	go syncer().NotifyManager(ev)
}

// notifyGameExit : partie terminée (durée, code de sortie), attendue au plus eventWait.
func notifyGameExit(name string, started time.Time, code int) {
	ev := gameEvent{Event: "exit", Game: name, At: time.Now().Unix(), Started: started.Unix(), Code: &code}
	done := make(chan struct{})
	go func() {
		syncer().NotifyManager(ev)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(eventWait):
	}
}
