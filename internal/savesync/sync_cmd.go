package savesync

// Commande --sync : synchronisation manuelle des sauvegardes de partie entre machines du réseau
// local (SteamBox, AIBox, Steam Deck), par l'agent HTTPS (--pair sur l'autre machine). Rien ne se
// fait au lancement d'un jeu : on synchronise quand on le demande. Le cœur est dans sync.go, le
// client dans sync_http.go ; ce fichier porte la commande, ses messages et la logique d'un jeu.
//
//   --sync wsq://…            associer une machine (code affiché par --pair sur l'autre)
//   --sync                    synchroniser tous les jeux avec chaque machine associée
//   --sync <jeu>              un jeu
//   --sync status [jeu]       ce que ferait --sync, sans rien changer
//   --sync forget <machine>   retirer une machine associée
//   --sync dir                dossiers déclarés et jeux Switch d'Eden
//   --sync dir add <nom> <chemin> | dir forget <nom>   déclarer ou retirer un dossier
//   --sync auto [on | off]    synchro avec le manager : envoi à la sortie d'un jeu (sync_auto.go), réception
//                             de sa version la plus récente (agent lancé) ; état sans argument

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"wsquashfs-launcher/internal/ui"
)

const syncUsage = "wsquashfs-launcher --sync [jeu | status [jeu] | forget <machine> | dir … | auto [on | off] | wsq://…]"

// syncAutoUsage : syntaxe de --sync auto.
const syncAutoUsage = "wsquashfs-launcher --sync auto [on | off]"

// fail : message d'erreur, code de sortie 1.
func fail(msg string) int {
	ui.Error(msg)
	return 1
}

// usage : rappel de syntaxe, code de sortie 1.
func usage(syntax string) int {
	ui.Usage(syntax)
	return 1
}

// Run : commande --sync [...]. Voir l'en-tête du fichier. Retourne le code de sortie : les erreurs
// sont affichées ici, rien n'arrête le processus.
func (s *Syncer) Run(args []string) int {
	cfg := LoadConfig()
	if len(args) == 0 {
		return s.syncRun(cfg, "", false)
	}
	switch first := args[0]; {
	case strings.HasPrefix(first, "wsq://"):
		if len(args) != 1 {
			return usage(syncUsage)
		}
		return s.syncPair(cfg, first)
	case first == "forget":
		if len(args) != 2 {
			return usage("wsquashfs-launcher --sync forget <machine>")
		}
		return s.syncForget(cfg, args[1])
	case first == "dir":
		return s.syncDirCmd(cfg, args[1:])
	case first == "auto":
		return s.syncAutoCmd(cfg, args[1:])
	case first == "status":
		if len(args) > 2 {
			return usage("wsquashfs-launcher --sync status [jeu]")
		}
		game := ""
		if len(args) == 2 {
			var ok bool
			if game, ok = syncGameArg(args[1]); !ok {
				return 1
			}
		}
		return s.syncRun(cfg, game, true)
	}
	if len(args) != 1 {
		return usage(syncUsage)
	}
	game, ok := syncGameArg(args[0])
	if !ok {
		return 1
	}
	return s.syncRun(cfg, game, false)
}

// syncGameArg : nom de synchro d'un argument : dossier:<nom> ou switch:<TitleID> tel quel, sinon
// nom de jeu (image, dossier ou nom). Si le nom n'est pas valide : message affiché, faux.
func syncGameArg(arg string) (string, bool) {
	if strings.HasPrefix(arg, syncDirPrefix) || strings.HasPrefix(arg, syncSwitchPrefix) {
		if !validSyncName(arg) {
			fail("nom invalide : " + arg)
			return "", false
		}
		return arg, true
	}
	game := gameNameOf(arg)
	if !ValidGame(game) {
		fail("jeu invalide : " + arg)
		return "", false
	}
	return game, true
}

// syncDirCmd : --sync dir [add <nom> <chemin> | forget <nom>].
func (s *Syncer) syncDirCmd(cfg Config, args []string) int {
	switch {
	case len(args) == 0:
		s.syncDirList(cfg)
		return 0
	case args[0] == "add" && len(args) == 3:
		next, err := AddDir(cfg, args[1], args[2])
		if err != nil {
			return fail(err.Error())
		}
		if err := SaveConfig(next); err != nil {
			return fail("configuration non enregistrée : " + err.Error())
		}
		for _, d := range next.Dirs {
			if d.Name == args[1] {
				ui.StatusLine(ui.OK, "Dossier déclaré : "+d.Name+" (~/"+HomeRel(d.Path)+")")
			}
		}
		ui.Note("Le déclarer aussi sur les autres machines (même nom, leur chemin), puis --sync.")
		return 0
	case args[0] == "forget" && len(args) == 2:
		next, found := RemoveDir(cfg, args[1])
		if !found {
			return fail("dossier inconnu : " + args[1])
		}
		if err := SaveConfig(next); err != nil {
			return fail("configuration non enregistrée : " + err.Error())
		}
		ui.StatusLine(ui.OK, "Dossier retiré : "+args[1]+" (ses fichiers restent)")
		return 0
	}
	return usage("wsquashfs-launcher --sync dir [add <nom> <chemin depuis le dossier personnel> | forget <nom>]")
}

// AutoSet : configuration avec la synchro automatique activée (on) ou désactivée. Activer sans
// manager déclaré est refusé : le manager déclare la machine lui-même (PUT /v1/saves/manager).
func AutoSet(cfg Config, on bool) (Config, error) {
	if on {
		if _, ok := ManagerPeer(cfg); !ok {
			return cfg, errors.New("aucun manager déclaré : associer la machine dans wsquashfs-manager d'abord")
		}
	}
	cfg.Auto = on
	return cfg, nil
}

// syncAutoCmd : --sync auto [on | off]. Sans argument, l'état ; on et off sont persistants et
// idempotents (déjà dans l'état demandé : message, code 0).
func (s *Syncer) syncAutoCmd(cfg Config, args []string) int {
	ui.Title("Synchronisation automatique", "avec le manager")
	if len(args) == 0 {
		state := "inactive"
		if cfg.Auto {
			state = "active"
		}
		manager := "aucun manager déclaré"
		if p, ok := ManagerPeer(cfg); ok {
			manager = p.Name
		}
		ui.KV(2, ui.Pair{"État", state}, ui.Pair{"Manager", manager})
		if !cfg.Auto {
			ui.Note("Activer : wsquashfs-launcher --sync auto on")
		}
		s.syncAutoAgentNote(cfg.Auto)
		return 0
	}
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return usage(syncAutoUsage)
	}
	on := args[0] == "on"
	next, err := AutoSet(cfg, on)
	if err != nil {
		return fail(err.Error())
	}
	if cfg.Auto == on {
		word := "désactivée"
		if on {
			word = "activée"
		}
		ui.StatusLine(ui.OK, "synchro automatique déjà "+word)
		s.syncAutoAgentNote(on)
		return 0
	}
	if err := SaveConfig(next); err != nil {
		return fail("configuration non enregistrée : " + err.Error())
	}
	if on {
		p, _ := ManagerPeer(next)
		ui.StatusLine(ui.OK, "Synchro automatique activée avec "+p.Name+" : envoi à la sortie d'un jeu, réception de sa version la plus récente")
		s.syncAutoAgentNote(true)
		return 0
	}
	ui.StatusLine(ui.OK, "Synchro automatique désactivée")
	return 0
}

// syncAutoAgentNote : synchro automatique active mais agent arrêté : l'envoi à la sortie d'un jeu
// marche quand même (le lanceur appelle le manager), mais le manager ne joint pas cette machine
// (pastille « auto », version la plus récente envoyée vers elle) : le dire, sans rien bloquer.
func (s *Syncer) syncAutoAgentNote(auto bool) {
	if !auto {
		return
	}
	if s.env.AgentRunning != nil && s.env.AgentRunning() {
		return
	}
	ui.Note("wsquashfs-launcher --agent pour recevoir les parties du manager et être visible dans le manager")
}

// syncDirList : dossiers déclarés, puis les jeux Switch d'Eden.
func (s *Syncer) syncDirList(cfg Config) {
	ui.Title("Synchronisation des sauvegardes", "dossiers et Switch")
	ui.Section("Dossiers déclarés")
	if len(cfg.Dirs) == 0 {
		ui.Note("Aucun. Déclarer : wsquashfs-launcher --sync dir add <nom> <chemin>")
	}
	for _, d := range cfg.Dirs {
		t := Target{Dir: d.Path, Plain: true}
		files := t.Files()
		where := HomeRel(d.Path)
		if !filepath.IsAbs(where) {
			where = "~/" + where
		}
		detail := where + " · vide"
		if len(files) > 0 {
			detail = where + " · " + ui.SizeText(SaveSize(files))
		}
		ui.KV(2, ui.Pair{d.Name, detail})
	}
	ui.Section("Switch (Eden)")
	data := EdenDataDir()
	switch user := edenSaveUser(); {
	case data == "":
		ui.Note("Eden absent (WSQUASHFS_EDEN_DIR pour un autre emplacement).")
	case user == "":
		ui.Note("Eden trouvé (" + data + "), sans profil : le lancer une fois.")
	default:
		titles := edenTitlesOf(user)
		ui.Note(fmt.Sprintf("%s · %d jeu(x), synchronisés jeu par jeu", data, len(titles)))
		for _, id := range titles {
			if name := s.edenTitleName(id); name != "" {
				ui.KV(2, ui.Pair{id, name})
			} else {
				ui.KV(2, ui.Pair{id, "(nom inconnu : TitleID absent des ROM et des gamelist.xml)"})
			}
		}
	}
}

// PeerID : identifiant local d'une machine associée, tenu dans l'état de synchro : les 32 premiers
// caractères hexadécimaux de son empreinte de certificat (valides pour ValidID).
func PeerID(peer Peer) string {
	fp := strings.ToLower(peer.Fingerprint)
	if len(fp) > 32 {
		fp = fp[:32]
	}
	return fp
}

// AddPeer : ajoute une machine, ou remplace celle de même empreinte ou de même adresse. Un nom
// déjà pris par une autre machine (deux machines au même nom d'hôte) reçoit l'adresse entre parenthèses.
func AddPeer(cfg Config, peer Peer) Config {
	kept := []Peer{}
	for _, p := range cfg.Peers {
		if p.Fingerprint == peer.Fingerprint || p.URL == peer.URL {
			continue
		}
		kept = append(kept, p)
	}
	for _, p := range kept {
		if strings.EqualFold(p.Name, peer.Name) {
			peer.Name += " (" + peer.URL + ")"
			break
		}
	}
	cfg.Peers = append(kept, peer)
	return cfg
}

// removeSyncPeer : retire la machine nommée (sans tenir compte de la casse). Faux si elle n'existe pas.
func removeSyncPeer(cfg Config, name string) (Config, bool) {
	kept := []Peer{}
	found := false
	for _, p := range cfg.Peers {
		if strings.EqualFold(p.Name, name) {
			found = true
			continue
		}
		kept = append(kept, p)
	}
	cfg.Peers = kept
	return cfg, found
}

// syncPeerNames : noms des machines associées, « aucune » si la liste est vide.
func syncPeerNames(cfg Config) string {
	if len(cfg.Peers) == 0 {
		return "aucune"
	}
	names := make([]string, 0, len(cfg.Peers))
	for _, p := range cfg.Peers {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

// syncPair : associe la machine dont le code (wsq://…) est donné, ou remplace son association. Le
// code (à usage unique) est échangé contre un jeton durable propre à cette machine, qui seul est gardé.
func (s *Syncer) syncPair(cfg Config, code string) int {
	peer, err := ParsePairCode(code)
	if err != nil {
		return fail(err.Error())
	}
	ui.Title("Association de machine", "réseau local")
	if s.env.CertFingerprint != nil {
		if fp, err := s.env.CertFingerprint(); err == nil && fp == peer.Fingerprint {
			return fail("c'est cette machine : coller le code affiché par --pair sur l'AUTRE machine")
		}
	}
	reply, err := s.NewClient(peer, cfg.ID).Pair()
	if err != nil {
		if IsUnreachable(err) {
			return fail(peer.URL + " injoignable : l'autre machine est-elle allumée, sur le même réseau, et son agent lancé (--agent) ?")
		}
		return fail(err.Error())
	}
	peer.Token = reply.Token
	peer.Name = reply.Host
	if peer.Name == "" {
		peer.Name = peer.URL
	}
	cfg = AddPeer(cfg, peer)
	if err := SaveConfig(cfg); err != nil {
		return fail("configuration impossible : " + err.Error())
	}
	ui.StatusLine(ui.OK, peer.Name+" enregistrée")
	ui.Note("Synchroniser : wsquashfs-launcher --sync")
	return 0
}

// syncForget : retire une machine associée.
func (s *Syncer) syncForget(cfg Config, name string) int {
	names := syncPeerNames(cfg)
	cfg, found := removeSyncPeer(cfg, name)
	if !found {
		return fail("aucune machine associée nommée « " + name + " » (machines : " + names + ")")
	}
	if err := SaveConfig(cfg); err != nil {
		return fail("configuration impossible : " + err.Error())
	}
	ui.Title("Machines associées", "réseau local")
	ui.StatusLine(ui.OK, name+" retirée")
	return 0
}

// syncNoPeers : aucune machine associée : explication des deux commandes d'association.
func syncNoPeers() {
	ui.Title("Synchronisation des sauvegardes", "réseau local")
	ui.Note("Aucune machine associée. Une fois, pour chaque autre machine :")
	ui.Note("sur l'autre machine (B), afficher son code (il attend l'association) :")
	ui.Code("wsquashfs-launcher --pair")
	ui.Note("sur cette machine (A), associer B avec le code affiché :")
	ui.Code("wsquashfs-launcher --sync wsq://…")
}

// syncStateGames : jeux ayant un état de synchro enregistré pour une machine associée.
func syncStateGames(stateRoot, peerID string) []string {
	entries, err := os.ReadDir(filepath.Join(stateRoot, peerID))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if g, ok := strings.CutSuffix(e.Name(), ".json"); ok && validSyncName(g) {
			out = append(out, g)
		}
	}
	return out
}

// syncRun : synchronise un jeu (game) ou tous les jeux (game vide) avec chaque machine associée.
// dryRun : rien n'est verrouillé ni écrit, le sens de chaque jeu est seulement annoncé.
func (s *Syncer) syncRun(cfg Config, game string, dryRun bool) int {
	if len(cfg.Peers) == 0 {
		syncNoPeers()
		return 0
	}
	sub := "réseau local"
	if dryRun {
		sub = "simulation : rien n'est modifié"
	}
	ui.Title("Synchronisation des sauvegardes", sub)
	stateRoot := s.StateRoot()
	local := map[string]bool{}
	for _, g := range s.Names() {
		local[g] = true
	}
	peers := append([]Peer{}, cfg.Peers...)
	slices.SortFunc(peers, func(a, b Peer) int { return cmp.Compare(a.Name, b.Name) })
	failed := false
	for _, peer := range peers {
		ui.Section(peer.Name)
		client := s.NewClient(peer, cfg.ID)
		remote, err := client.List()
		if err != nil {
			if IsUnreachable(err) {
				ui.StatusLine(ui.Skip, "injoignable (éteinte ou hors du réseau local)")
				continue
			}
			ui.StatusLine(ui.Fail, err.Error())
			failed = true
			continue
		}
		peerID := PeerID(peer)
		byName := map[string]RemoteSave{}
		games := map[string]bool{}
		for g := range local {
			games[g] = true
		}
		for _, r := range remote {
			if validSyncName(r.Name) {
				byName[r.Name] = r
				games[r.Name] = true
			}
		}
		for _, g := range syncStateGames(stateRoot, peerID) {
			games[g] = true
		}
		names := make([]string, 0, len(games))
		for g := range games {
			names = append(names, g)
		}
		slices.Sort(names)
		if game != "" {
			if !games[game] {
				ui.NewGames([]string{game}).Line(ui.Skip, game, "aucune sauvegarde ici ni sur "+peer.Name)
				continue
			}
			names = []string{game}
		}
		// Nom affiché : nom lisible d'un dossier ou d'un jeu Switch (d'ici, sinon de l'autre machine).
		labels := map[string]string{}
		shown := make([]string, 0, len(names))
		for _, name := range names {
			labels[name] = name
			if t, err := s.TargetOf(name); err == nil && t.Label != "" {
				labels[name] = t.Label
			} else if l := byName[name].Label; l != "" {
				labels[name] = l
			}
			shown = append(shown, labels[name])
		}
		g := ui.NewGames(shown)
		oneSide := 0
		for _, name := range names {
			// --sync sans jeu : un jeu qui n'a de sauvegardes que d'un côté n'est pas une
			// différence (machine à plus petit stockage, jeu pas installé). Le copier sur
			// l'autre machine : --sync "<jeu>", explicite.
			t, terr := s.TargetOf(name)
			if game == "" {
				here := terr == nil && t.Fingerprint(t.Files()) != ""
				if here != (byName[name].Fingerprint != "") {
					oneSide++
					continue
				}
			}
			if errors.Is(terr, ErrAbsent) && byName[name].Dir != "" && !dryRun {
				// Dossier déclaré sur l'autre machine, pas ici : déclaré avec son chemin, puis reçu.
				t, terr = s.DeclareFromPeer(name, byName[name].Dir)
			}
			if terr != nil {
				g.Line(ui.Skip, labels[name], "absent ici (dossier non déclaré : --sync dir add, ou Eden absent)")
				continue
			}
			st, msg := s.SyncTarget(client, peer.Name, peerID, stateRoot, name, t, byName[name], dryRun)
			g.Line(st, labels[name], msg)
			if st == ui.Fail {
				failed = true
			}
		}
		if oneSide > 0 {
			ui.StatusLine(ui.Skip, fmt.Sprintf("%d jeu(x) avec des sauvegardes d'un seul côté, non synchronisé(s) : "+
				"--sync \"<jeu>\" pour en copier un", oneSide))
		}
	}
	ui.Note("Réseau local uniquement. Versions écartées en cas de conflit : " + s.backupDir())
	if failed {
		return 1
	}
	return 0
}

// syncErrStatus : statut d'une erreur de synchro d'un jeu : jeu en cours sur l'autre machine
// (ignoré, la synchro reprendra plus tard), sinon échec.
func syncErrStatus(err error, peerName string) (ui.Status, string) {
	if errors.Is(err, ErrBusy) {
		return ui.Skip, "en cours sur " + peerName + ", ignoré"
	}
	return ui.Fail, err.Error()
}

// backupDir : dossier des backups, "" si l'environnement ne le dit pas.
func (s *Syncer) backupDir() string {
	if s.env.BackupDir == nil {
		return ""
	}
	return s.env.BackupDir()
}

// archivedNote : « (chemin) » après une archive, rien sans archive.
func archivedNote(path string) string {
	if path == "" {
		return ""
	}
	return " (" + path + ")"
}

// SyncTarget : synchronise UNE cible (jeu, dossier, jeu Switch) avec une machine associée.
// remote : son état sur la machine distante (liste). dryRun : rien n'est verrouillé ni écrit.
// Retourne le statut et le texte de la ligne.
func (s *Syncer) SyncTarget(c *Client, peerName, peerID, stateRoot, name string, t Target, remote RemoteSave, dryRun bool) (ui.Status, string) {
	if t.game && s.env.GameUsesSaveDir != nil && s.env.GameUsesSaveDir(name) {
		return ui.Skip, "sauvegardes hors du prefix (SAVEDIR/SAVEFILES), non synchronisées"
	}
	var lock *os.File
	if !dryRun {
		var f *os.File
		var ok bool
		f, ok = t.Lock()
		if !ok {
			return ui.Skip, "en cours ici, ignoré"
		}
		lock = f
	}
	if lock != nil {
		defer lock.Close()
	}
	files := t.Files()
	fp := t.Fingerprint(files)
	if fp == "" && remote.Fingerprint == "" {
		return ui.Skip, "aucune sauvegarde ici ni sur " + peerName
	}
	state := LoadState(stateRoot, peerID, name).Fingerprint
	action := decideSync(fp, state, NewestSave(files), remote.Fingerprint, remote.State, remote.Newest, t.Content(files), remote.Content)
	if dryRun {
		return ui.Info, action.String()
	}
	switch action {
	case syncUpToDate:
		if state != fp {
			if err := SaveState(stateRoot, peerID, name, fp); err != nil {
				return ui.Fail, err.Error()
			}
			if err := c.Ack(name, fp); err != nil && IsUnreachable(err) {
				return syncErrStatus(err, peerName)
			}
		}
		return ui.OK, "à jour"

	case syncConflict:
		// Les deux ont changé : rien n'est transféré ni écrasé. La copie d'ici part au manager
		// (version de conflit), où l'utilisateur départage.
		msg := "conflit avec " + peerName + " (les deux ont changé) : à départager dans le manager"
		return ui.Skip, msg + s.sendConflictToManager(peerName, name, t)

	case syncSend:
		_, _, err := c.Send(t)
		if err != nil {
			return syncErrStatus(err, peerName)
		}
		if err := SaveState(stateRoot, peerID, name, t.Fingerprint(t.Files())); err != nil {
			return ui.Fail, err.Error()
		}
		return ui.OK, "envoyé à " + peerName

	case syncReceive:
		// Version d'ici modifiée depuis la dernière synchro (ou état inconnu, première synchro) :
		// mise de côté avant d'être remplacée.
		archivedLocal := ""
		if fp != "" && fp != state {
			path, err := t.ArchiveLocal()
			if err != nil {
				return ui.Fail, "version d'ici non archivée, rien reçu : " + err.Error()
			}
			archivedLocal = path
		}
		rfp, err := c.Fetch(t)
		if err != nil {
			return syncErrStatus(err, peerName)
		}
		if err := c.Ack(name, rfp); err != nil && IsUnreachable(err) {
			return ui.Fail, "reçu, mais l'état n'est pas confirmé sur " + peerName + " : " + err.Error()
		}
		if err := SaveState(stateRoot, peerID, name, t.Fingerprint(t.Files())); err != nil {
			return ui.Fail, err.Error()
		}
		return ui.OK, "reçu de " + peerName + archivedNote(archivedLocal)
	}
	return ui.Fail, "action de synchro inconnue"
}
