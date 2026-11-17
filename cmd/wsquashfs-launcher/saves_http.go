package main

// Les routes /v1/saves/* (synchro --sync, sync.go) lisent et écrivent les
// sauvegardes de partie, bornées au périmètre de sync.go : jamais hors de
// users/, jamais les caches ni les comptes système.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// savesPutTimeout : délai de lecture d'une archive de sauvegardes envoyée (ReadTimeout de
// l'agent : 60 s, trop court pour une grosse sauvegarde ou un réseau lent).
const savesPutTimeout = 10 * time.Minute

// syncMaxBody : taille maximale d'une archive reçue (--sync). Variable pour les tests.
var syncMaxBody int64 = 2 << 30

// syncStateRoot : état des synchros, par machine cliente (dossier caché de savesDir).
func syncStateRoot() string {
	return filepath.Join(conf.savesDir, ".sync")
}

// syncMachine : authentification, puis identifiant de la machine cliente obligatoire
// (en-tête X-Wsq-Machine) : l'état de synchro est tenu par machine.
func (s *agentServer) syncMachine(next http.HandlerFunc) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		if !validSyncID(r.Header.Get("X-Wsq-Machine")) {
			writeDetail(w, http.StatusBadRequest, "identifiant de machine absent ou invalide")
			return
		}
		next(w, r)
	})
}

// remoteSave : un jeu tel que la machine le voit pour la synchro (routes /v1/saves/list).
type remoteSave struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"` // dossier déclaré, jeu Switch : nom lisible
	Dir         string `json:"dir,omitempty"`   // dossier déclaré : chemin à partir de HOME (propagé à la cible)
	Fingerprint string `json:"fingerprint"`
	Content     string `json:"content"` // empreinte de contenu, sans les dates ("" : inconnue)
	Newest      int64  `json:"newest"`
	Size        int64  `json:"size"`
	State       string `json:"state"`
}

// savesList : jeux à sauvegardes, plus ceux qui ont un état pour cette machine.
func (s *agentServer) savesList(w http.ResponseWriter, r *http.Request) {
	machine := r.Header.Get("X-Wsq-Machine")
	// Jeux avec un état pour cette machine : listés même vides (la synchro doit le savoir).
	withState := map[string]bool{}
	if entries, err := os.ReadDir(filepath.Join(syncStateRoot(), machine)); err == nil {
		for _, e := range entries {
			if g, ok := strings.CutSuffix(e.Name(), ".json"); ok {
				withState[g] = true
			}
		}
	}
	// Même liste que --sync (syncNames : jeux, dossiers déclarés, jeux Switch), plus ce qui n'a
	// qu'un état pour cette machine.
	names := map[string]bool{}
	for _, g := range syncNames() {
		names[g] = true
	}
	for g := range withState {
		names[g] = true
	}
	games := []remoteSave{}
	for name := range names {
		t, err := syncTargetOf(name)
		if err != nil {
			continue
		}
		files := t.files()
		fp := t.fingerprint(files)
		if fp == "" && !withState[name] {
			continue
		}
		st := loadSyncState(syncStateRoot(), machine, name)
		games = append(games, remoteSave{Name: name, Label: t.Label, Dir: t.relDir(), Fingerprint: fp, Content: t.content(files), Newest: newestSave(files), Size: saveSize(files), State: st.Fingerprint})
	}
	sort.Slice(games, func(i, j int) bool { return games[i].Name < games[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"games": games})
}

// savesGet : archive des sauvegardes d'un jeu (tar.gz). L'archive est écrite dans un fichier
// temporaire d'abord : l'empreinte et le profil partent dans les en-têtes, avant le corps.
func (s *agentServer) savesGet(w http.ResponseWriter, r *http.Request) {
	game := r.URL.Query().Get("game")
	t, err := syncTargetOf(game)
	if err != nil {
		writeSyncTargetErr(w, err)
		return
	}
	lock, ok := t.lock()
	if !ok {
		writeDetail(w, http.StatusConflict, "jeu en cours")
		return
	}
	if lock != nil {
		defer lock.Close()
	}
	files := t.files()
	if len(files) == 0 {
		writeDetail(w, http.StatusNotFound, "aucune sauvegarde pour ce jeu")
		return
	}
	fp := t.fingerprint(files)
	tmp, err := syncTempFile("wsq-sync-*.tar.gz")
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "erreur interne de l'agent")
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	profile, err := t.writeArchive(tmp, files)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "archive impossible : "+game)
		return
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "erreur interne de l'agent")
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeDetail(w, http.StatusInternalServerError, "erreur interne de l'agent")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", fmt.Sprint(size))
	w.Header().Set("X-Wsq-Fingerprint", fp)
	if profile != "" {
		w.Header().Set("X-Wsq-Profile", profile)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, tmp)
}

// savesPut : reçoit une archive et remplace le périmètre du jeu. Si le périmètre local a changé
// depuis la dernière synchro avec cette machine, il est archivé d'abord (dossier backups) : c'est
// la version perdante du conflit, rien ne doit disparaître. Sans archive, rien n'est remplacé.
func (s *agentServer) savesPut(w http.ResponseWriter, r *http.Request) {
	game := r.URL.Query().Get("game")
	t, err := syncTargetOf(game)
	if errors.Is(err, errSyncAbsent) && r.Header.Get(syncDirHeader) != "" {
		// Dossier déclaré sur l'envoyeur, pas ici : déclaré avec son chemin (à partir de HOME), comme
		// un jeu reçu pour la première fois.
		rel, uerr := url.PathUnescape(r.Header.Get(syncDirHeader))
		if uerr != nil {
			writeDetail(w, http.StatusBadRequest, "chemin du dossier illisible")
			return
		}
		s.syncCfgMu.Lock()
		t, err = declareFromPeer(game, rel)
		s.syncCfgMu.Unlock()
		if err != nil && !errors.Is(err, errSyncAbsent) && !errors.Is(err, errSyncInvalid) {
			writeDetail(w, http.StatusBadRequest, "dossier non déclaré ici : "+err.Error())
			return
		}
	}
	if err != nil {
		writeSyncTargetErr(w, err)
		return
	}
	machine := r.Header.Get("X-Wsq-Machine")
	lock, ok := t.lock()
	if !ok {
		writeDetail(w, http.StatusConflict, "jeu en cours")
		return
	}
	if lock != nil {
		defer lock.Close()
	}
	cur := t.fingerprint(t.files())
	archived := ""
	if cur != "" && cur != loadSyncState(syncStateRoot(), machine, game).Fingerprint {
		// Le membre est le nom réel du dossier : une mise de côté (mode copy) ne s'appelle pas « users ».
		path, err := t.archiveLocal()
		if err != nil {
			writeDetail(w, http.StatusInternalServerError, "archivage de la version locale impossible, rien remplacé")
			return
		}
		archived = path
	}
	// Une grosse sauvegarde peut être longue à recevoir : délai de lecture propre à cet envoi,
	// compté à partir d'ici (l'archivage local ci-dessus n'y entre pas).
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(savesPutTimeout))
	r.Body = http.MaxBytesReader(w, r.Body, syncMaxBody)
	if err := t.apply(r.Body, r.Header.Get("X-Wsq-Profile")); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeDetail(w, http.StatusRequestEntityTooLarge, "archive trop volumineuse")
			return
		}
		writeDetail(w, http.StatusBadRequest, "archive refusée : "+err.Error())
		return
	}
	fp := t.fingerprint(t.files())
	for _, key := range stateKeysOf(machine) {
		if err := saveSyncState(syncStateRoot(), key, game, fp); err != nil {
			writeDetail(w, http.StatusInternalServerError, "état de synchro non enregistré")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"fingerprint": fp, "archived": archived})
}

// stateKeysOf : clés sous lesquelles l'état de synchro d'un appelant est tenu. Le manager (pair
// déclaré par PUT /v1/saves/manager, identifiant de ses envois) l'est aussi sous syncPeerID : c'est
// la clé que lit --sync auto à la sortie d'un jeu (sync_auto.go).
func stateKeysOf(machine string) []string {
	keys := []string{machine}
	if peer, ok := managerPeer(loadSyncConfig()); ok && peer.MachineID == machine {
		if key := syncPeerID(peer); validSyncID(key) {
			keys = append(keys, key)
		}
	}
	return keys
}

// savesDelete : supprime les sauvegardes d'un jeu sur cette machine (le manager, par l'agent). Le
// périmètre est archivé d'abord (dossier backups) : si l'archivage échoue, rien n'est effacé. Seuls
// les fichiers du périmètre partent, puis les dossiers devenus vides ; caches et autres fichiers
// restent. L'état de synchro de la machine appelante pour ce jeu est effacé ; ceux des autres machines
// restent.
func (s *agentServer) savesDelete(w http.ResponseWriter, r *http.Request) {
	game := r.URL.Query().Get("game")
	t, err := syncTargetOf(game)
	if errors.Is(err, errSyncAbsent) {
		// Rien de déclaré ici : rien à supprimer (comme un jeu sans sauvegarde).
		writeJSON(w, http.StatusOK, map[string]any{"archived": "", "removed": 0})
		return
	}
	if err != nil {
		writeSyncTargetErr(w, err)
		return
	}
	machine := r.Header.Get("X-Wsq-Machine")
	lock, ok := t.lock()
	if !ok {
		writeDetail(w, http.StatusConflict, "jeu en cours")
		return
	}
	if lock != nil {
		defer lock.Close()
	}
	files := t.files()
	if len(files) == 0 {
		if err := removeStateKeys(machine, game); err != nil {
			writeDetail(w, http.StatusInternalServerError, "état de synchro non supprimé")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"archived": "", "removed": 0})
		return
	}
	archived, err := t.archiveLocal()
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "archivage impossible, rien supprimé")
		return
	}
	removed, err := t.removeFiles(files)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "suppression impossible ("+err.Error()+"), archive : "+archived)
		return
	}
	if err := removeStateKeys(machine, game); err != nil {
		writeDetail(w, http.StatusInternalServerError, "état de synchro non supprimé, archive : "+archived)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"archived": archived, "removed": removed})
}

// removeStateKeys : efface l'état de synchro d'un appelant pour ce jeu (toutes ses clés, voir stateKeysOf).
func removeStateKeys(machine, game string) error {
	for _, key := range stateKeysOf(machine) {
		if err := removeSyncState(syncStateRoot(), key, game); err != nil {
			return err
		}
	}
	return nil
}

// removeEmptyDirs : supprime dir, puis ses parents devenus vides, jusqu'à (exclu) stop. Un dossier
// non vide arrête la remontée.
func removeEmptyDirs(dir, stop string) {
	for dir != stop && strings.HasPrefix(dir, stop+string(filepath.Separator)) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// syncStateRequest : corps de POST /v1/saves/state.
type syncStateRequest struct {
	Game        string `json:"game"`
	Fingerprint string `json:"fingerprint"`
}

// savesState : la machine cliente confirme l'empreinte commune après une réception. Refusé si le
// jeu a changé sur cette machine entre-temps.
func (s *agentServer) savesState(w http.ResponseWriter, r *http.Request) {
	var req syncStateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	t, err := syncTargetOf(req.Game)
	if err != nil {
		writeSyncTargetErr(w, err)
		return
	}
	cur := t.fingerprint(t.files())
	if cur != req.Fingerprint {
		writeDetail(w, http.StatusConflict, "le jeu a changé sur cette machine depuis la lecture : relancer --sync")
		return
	}
	for _, key := range stateKeysOf(r.Header.Get("X-Wsq-Machine")) {
		if err := saveSyncState(syncStateRoot(), key, req.Game, cur); err != nil {
			writeDetail(w, http.StatusInternalServerError, "état de synchro non enregistré")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"fingerprint": cur})
}

// writeSyncTargetErr : réponse d'erreur de syncTargetOf (400 nom invalide, 404 absent d'ici).
func writeSyncTargetErr(w http.ResponseWriter, err error) {
	code, msg := syncTargetHTTP(err)
	writeDetail(w, code, msg)
}

// ── Dossiers déclarés (manager) ─────────────────────────────────────────

// syncDirsReply : dossiers déclarés et dossier d'Eden détecté ("" si absent), chemins donnés à
// partir de HOME (« .local/share/x »).
func syncDirsReply() map[string]any {
	dirs := []syncDir{}
	for _, d := range loadSyncConfig().Dirs {
		dirs = append(dirs, syncDir{Name: d.Name, Path: homeRel(d.Path)})
	}
	return map[string]any{"dirs": dirs, "eden": homeRel(edenDataDir())}
}

// savesDirs : GET /v1/saves/dirs.
func (s *agentServer) savesDirs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, syncDirsReply())
}

// savesDirPut : PUT /v1/saves/dirs {name, path} : déclare (ou remplace) un dossier à synchroniser,
// sous HOME. Répond la liste à jour.
func (s *agentServer) savesDirPut(w http.ResponseWriter, r *http.Request) {
	var req syncDir
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req); err != nil {
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	s.syncCfgMu.Lock()
	defer s.syncCfgMu.Unlock()
	cfg, err := addSyncDir(loadSyncConfig(), strings.TrimSpace(req.Name), strings.TrimSpace(req.Path))
	if err != nil {
		writeDetail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := saveSyncConfig(cfg); err != nil {
		writeDetail(w, http.StatusInternalServerError, "configuration non enregistrée")
		return
	}
	writeJSON(w, http.StatusOK, syncDirsReply())
}

// savesManagerPut : PUT /v1/saves/manager {name, url, token, fingerprint} : le manager se déclare comme
// pair de cette machine (sync.json, Manager). Remplace le manager déclaré précédemment. Répond son
// empreinte et l'état de la synchro automatique.
func (s *agentServer) savesManagerPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		Token       string `json:"token"`
		Fingerprint string `json:"fingerprint"`
		UI          string `json:"ui"` // facultatif : adresse de l'interface web, pour --tray
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req); err != nil {
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	peerURL, err := managerURL(req.URL)
	if err != nil {
		writeDetail(w, http.StatusBadRequest, err.Error())
		return
	}
	if !pairTokenRe.MatchString(req.Token) {
		writeDetail(w, http.StatusBadRequest, "jeton du manager absent ou mal formé")
		return
	}
	if !pairFPRe.MatchString(req.Fingerprint) {
		writeDetail(w, http.StatusBadRequest, "empreinte du manager invalide (64 caractères hexadécimaux)")
		return
	}
	ui, err := managerUIURL(req.UI)
	if err != nil {
		writeDetail(w, http.StatusBadRequest, err.Error())
		return
	}
	name := cleanMachineName(req.Name)
	if name == "" {
		name = peerURL
	}
	peer := syncPeer{
		Name:        name,
		URL:         peerURL,
		Token:       req.Token,
		Fingerprint: strings.ToUpper(req.Fingerprint),
		Manager:     true,
		MachineID:   r.Header.Get("X-Wsq-Machine"),
		UI:          ui,
	}
	s.syncCfgMu.Lock()
	defer s.syncCfgMu.Unlock()
	cfg := loadSyncConfig()
	kept := []syncPeer{}
	for _, p := range cfg.Peers {
		if !p.Manager {
			kept = append(kept, p)
		}
	}
	cfg.Peers = kept
	cfg = addSyncPeer(cfg, peer)
	if err := saveSyncConfig(cfg); err != nil {
		writeDetail(w, http.StatusInternalServerError, "configuration non enregistrée")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"manager": peer.Fingerprint, "auto": cfg.Auto})
}

// savesDirDelete : DELETE /v1/saves/dirs?name= : retire la déclaration (les fichiers restent).
func (s *agentServer) savesDirDelete(w http.ResponseWriter, r *http.Request) {
	s.syncCfgMu.Lock()
	defer s.syncCfgMu.Unlock()
	cfg, found := removeSyncDir(loadSyncConfig(), r.URL.Query().Get("name"))
	if !found {
		writeDetail(w, http.StatusNotFound, "dossier inconnu")
		return
	}
	if err := saveSyncConfig(cfg); err != nil {
		writeDetail(w, http.StatusInternalServerError, "configuration non enregistrée")
		return
	}
	writeJSON(w, http.StatusOK, syncDirsReply())
}

// managerUIURL : adresse de l'interface du manager (PUT /v1/saves/manager, champ ui) : "" (non
// donnée) ou une URL http(s) avec hôte, 512 caractères au plus.
func managerUIURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 512 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", errors.New("adresse de l'interface du manager invalide (http(s)://hôte[:port][/chemin])")
	}
	return u.String(), nil
}
