package main

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
)

const syncUsage = "wsquashfs-launcher --sync [jeu | status [jeu] | forget <machine> | dir … | auto [on | off] | wsq://…]"

// syncAutoUsage : syntaxe de --sync auto.
const syncAutoUsage = "wsquashfs-launcher --sync auto [on | off]"

// syncLockGame : verrou local d'un jeu (lockIdleGame). Variable : les tests simulent deux machines
// dans un même processus, qui partagent les globales (et donc le fichier de verrou) ; ils la
// remplacent côté client.
var syncLockGame = lockIdleGame

// cmdSync : --sync [...]. Voir l'en-tête du fichier.
func cmdSync(args []string) int {
	cfg := loadSyncConfig()
	if len(args) == 0 {
		return syncRun(cfg, "", false)
	}
	switch first := args[0]; {
	case strings.HasPrefix(first, "wsq://"):
		if len(args) != 1 {
			usageError(syncUsage)
		}
		return syncPair(cfg, first)
	case first == "forget":
		if len(args) != 2 {
			usageError("wsquashfs-launcher --sync forget <machine>")
		}
		return syncForget(cfg, args[1])
	case first == "dir":
		return syncDirCmd(cfg, args[1:])
	case first == "auto":
		return syncAutoCmd(cfg, args[1:])
	case first == "status":
		if len(args) > 2 {
			usageError("wsquashfs-launcher --sync status [jeu]")
		}
		game := ""
		if len(args) == 2 {
			game = syncGameArg(args[1])
		}
		return syncRun(cfg, game, true)
	}
	if len(args) != 1 {
		usageError(syncUsage)
	}
	return syncRun(cfg, syncGameArg(args[0]), false)
}

// syncGameArg : nom de synchro d'un argument : dossier:<nom> ou switch:<TitleID> tel quel, sinon
// nom de jeu (image, dossier ou nom) ; sortie si le nom n'est pas valide.
func syncGameArg(arg string) string {
	if strings.HasPrefix(arg, syncDirPrefix) || strings.HasPrefix(arg, syncSwitchPrefix) {
		if !validSyncName(arg) {
			die("nom invalide : " + arg)
		}
		return arg
	}
	game := gameNameOf(arg)
	if !validSyncGame(game) {
		die("jeu invalide : " + arg)
	}
	return game
}

// syncDirCmd : --sync dir [add <nom> <chemin> | forget <nom>].
func syncDirCmd(cfg syncConfig, args []string) int {
	switch {
	case len(args) == 0:
		syncDirList(cfg)
		return 0
	case args[0] == "add" && len(args) == 3:
		next, err := addSyncDir(cfg, args[1], args[2])
		if err != nil {
			die(err.Error())
		}
		if err := saveSyncConfig(next); err != nil {
			die("configuration non enregistrée : " + err.Error())
		}
		for _, d := range next.Dirs {
			if d.Name == args[1] {
				uiStatusLine(stOK, "Dossier déclaré : "+d.Name+" (~/"+homeRel(d.Path)+")")
			}
		}
		uiNote("Le déclarer aussi sur les autres machines (même nom, leur chemin), puis --sync.")
		return 0
	case args[0] == "forget" && len(args) == 2:
		next, found := removeSyncDir(cfg, args[1])
		if !found {
			die("dossier inconnu : " + args[1])
		}
		if err := saveSyncConfig(next); err != nil {
			die("configuration non enregistrée : " + err.Error())
		}
		uiStatusLine(stOK, "Dossier retiré : "+args[1]+" (ses fichiers restent)")
		return 0
	}
	usageError("wsquashfs-launcher --sync dir [add <nom> <chemin depuis le dossier personnel> | forget <nom>]")
	return 1
}

// syncAutoSet : configuration avec la synchro automatique activée (on) ou désactivée. Activer sans
// manager déclaré est refusé : le manager déclare la machine lui-même (PUT /v1/saves/manager).
func syncAutoSet(cfg syncConfig, on bool) (syncConfig, error) {
	if on {
		if _, ok := managerPeer(cfg); !ok {
			return cfg, errors.New("aucun manager déclaré : associer la machine dans wsquashfs-manager d'abord")
		}
	}
	cfg.Auto = on
	return cfg, nil
}

// syncAutoCmd : --sync auto [on | off]. Sans argument, l'état ; on et off sont persistants et
// idempotents (déjà dans l'état demandé : message, code 0).
func syncAutoCmd(cfg syncConfig, args []string) int {
	uiTitle("Synchronisation automatique", "avec le manager")
	if len(args) == 0 {
		state := "inactive"
		if cfg.Auto {
			state = "active"
		}
		manager := "aucun manager déclaré"
		if p, ok := managerPeer(cfg); ok {
			manager = p.Name
		}
		uiKV(2, kv{"État", state}, kv{"Manager", manager})
		if !cfg.Auto {
			uiNote("Activer : wsquashfs-launcher --sync auto on")
		}
		syncAutoAgentNote(cfg.Auto)
		return 0
	}
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		usageError(syncAutoUsage)
	}
	on := args[0] == "on"
	next, err := syncAutoSet(cfg, on)
	if err != nil {
		die(err.Error())
	}
	if cfg.Auto == on {
		word := "désactivée"
		if on {
			word = "activée"
		}
		uiStatusLine(stOK, "synchro automatique déjà "+word)
		syncAutoAgentNote(on)
		return 0
	}
	if err := saveSyncConfig(next); err != nil {
		die("configuration non enregistrée : " + err.Error())
	}
	if on {
		p, _ := managerPeer(next)
		uiStatusLine(stOK, "Synchro automatique activée avec "+p.Name+" : envoi à la sortie d'un jeu, réception de sa version la plus récente")
		syncAutoAgentNote(true)
		return 0
	}
	uiStatusLine(stOK, "Synchro automatique désactivée")
	return 0
}

// syncAutoAgentNote : synchro automatique active mais agent arrêté : l'envoi à la sortie d'un jeu
// marche quand même (le lanceur appelle le manager), mais le manager ne joint pas cette machine
// (pastille « auto », version la plus récente envoyée vers elle) : le dire, sans rien bloquer.
func syncAutoAgentNote(auto bool) {
	if !auto {
		return
	}
	if _, running := runningAgentPid(); running {
		return
	}
	uiNote("wsquashfs-launcher --agent pour recevoir les parties du manager et être visible dans le manager")
}

// syncDirList : dossiers déclarés, puis les jeux Switch d'Eden.
func syncDirList(cfg syncConfig) {
	uiTitle("Synchronisation des sauvegardes", "dossiers et Switch")
	uiSection("Dossiers déclarés")
	if len(cfg.Dirs) == 0 {
		uiNote("Aucun. Déclarer : wsquashfs-launcher --sync dir add <nom> <chemin>")
	}
	for _, d := range cfg.Dirs {
		t := syncTarget{Dir: d.Path, Plain: true}
		files := t.files()
		where := homeRel(d.Path)
		if !filepath.IsAbs(where) {
			where = "~/" + where
		}
		detail := where + " · vide"
		if len(files) > 0 {
			detail = where + " · " + sizeText(saveSize(files))
		}
		uiKV(2, kv{d.Name, detail})
	}
	uiSection("Switch (Eden)")
	data := edenDataDir()
	switch user := edenSaveUser(); {
	case data == "":
		uiNote("Eden absent (WSQUASHFS_EDEN_DIR pour un autre emplacement).")
	case user == "":
		uiNote("Eden trouvé (" + data + "), sans profil : le lancer une fois.")
	default:
		titles := edenTitlesOf(user)
		uiNote(fmt.Sprintf("%s · %d jeu(x), synchronisés jeu par jeu", data, len(titles)))
		for _, id := range titles {
			if name := edenTitleName(id); name != "" {
				uiKV(2, kv{id, name})
			} else {
				uiKV(2, kv{id, "(nom inconnu : TitleID absent des ROM et des gamelist.xml)"})
			}
		}
	}
}

// syncPeerID : identifiant local d'une machine associée, tenu dans l'état de synchro : les 32 premiers
// caractères hexadécimaux de son empreinte de certificat (valides pour validSyncID).
func syncPeerID(peer syncPeer) string {
	fp := strings.ToLower(peer.Fingerprint)
	if len(fp) > 32 {
		fp = fp[:32]
	}
	return fp
}

// addSyncPeer : ajoute une machine, ou remplace celle de même empreinte ou de même adresse. Un nom
// déjà pris par une autre machine (deux machines au même nom d'hôte) reçoit l'adresse entre parenthèses.
func addSyncPeer(cfg syncConfig, peer syncPeer) syncConfig {
	kept := []syncPeer{}
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
func removeSyncPeer(cfg syncConfig, name string) (syncConfig, bool) {
	kept := []syncPeer{}
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
func syncPeerNames(cfg syncConfig) string {
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
func syncPair(cfg syncConfig, code string) int {
	peer, err := parsePairCode(code)
	if err != nil {
		die(err.Error())
	}
	uiTitle("Association de machine", "réseau local")
	if fp, err := certFingerprint(); err == nil && fp == peer.Fingerprint {
		die("c'est cette machine : coller le code affiché par --pair sur l'AUTRE machine")
	}
	reply, err := newSyncClient(peer, cfg.ID).pair()
	if err != nil {
		if isUnreachable(err) {
			die(peer.URL + " injoignable : l'autre machine est-elle allumée, sur le même réseau, et son agent lancé (--agent) ?")
		}
		die(err.Error())
	}
	peer.Token = reply.Token
	peer.Name = reply.Host
	if peer.Name == "" {
		peer.Name = peer.URL
	}
	cfg = addSyncPeer(cfg, peer)
	if err := saveSyncConfig(cfg); err != nil {
		die("configuration impossible : " + err.Error())
	}
	uiStatusLine(stOK, peer.Name+" enregistrée")
	uiNote("Synchroniser : wsquashfs-launcher --sync")
	return 0
}

// syncForget : retire une machine associée.
func syncForget(cfg syncConfig, name string) int {
	names := syncPeerNames(cfg)
	cfg, found := removeSyncPeer(cfg, name)
	if !found {
		die("aucune machine associée nommée « " + name + " » (machines : " + names + ")")
	}
	if err := saveSyncConfig(cfg); err != nil {
		die("configuration impossible : " + err.Error())
	}
	uiTitle("Machines associées", "réseau local")
	uiStatusLine(stOK, name+" retirée")
	return 0
}

// syncNoPeers : aucune machine associée : explication des deux commandes d'association.
func syncNoPeers() {
	uiTitle("Synchronisation des sauvegardes", "réseau local")
	uiNote("Aucune machine associée. Une fois, pour chaque autre machine :")
	uiNote("sur l'autre machine (B), afficher son code (il attend l'association) :")
	uiCode("wsquashfs-launcher --pair")
	uiNote("sur cette machine (A), associer B avec le code affiché :")
	uiCode("wsquashfs-launcher --sync wsq://…")
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
func syncRun(cfg syncConfig, game string, dryRun bool) int {
	if len(cfg.Peers) == 0 {
		syncNoPeers()
		return 0
	}
	sub := "réseau local"
	if dryRun {
		sub = "simulation : rien n'est modifié"
	}
	uiTitle("Synchronisation des sauvegardes", sub)
	stateRoot := syncStateRoot()
	local := map[string]bool{}
	for _, g := range syncNames() {
		local[g] = true
	}
	peers := append([]syncPeer{}, cfg.Peers...)
	slices.SortFunc(peers, func(a, b syncPeer) int { return cmp.Compare(a.Name, b.Name) })
	failed := false
	for _, peer := range peers {
		uiSection(peer.Name)
		client := newSyncClient(peer, cfg.ID)
		remote, err := client.list()
		if err != nil {
			if isUnreachable(err) {
				uiStatusLine(stSkip, "injoignable (éteinte ou hors du réseau local)")
				continue
			}
			uiStatusLine(stFail, err.Error())
			failed = true
			continue
		}
		peerID := syncPeerID(peer)
		byName := map[string]remoteSave{}
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
				newUIGames([]string{game}).line(stSkip, game, "aucune sauvegarde ici ni sur "+peer.Name)
				continue
			}
			names = []string{game}
		}
		// Nom affiché : nom lisible d'un dossier ou d'un jeu Switch (d'ici, sinon de l'autre machine).
		labels := map[string]string{}
		shown := make([]string, 0, len(names))
		for _, name := range names {
			labels[name] = name
			if t, err := syncTargetOf(name); err == nil && t.Label != "" {
				labels[name] = t.Label
			} else if l := byName[name].Label; l != "" {
				labels[name] = l
			}
			shown = append(shown, labels[name])
		}
		g := newUIGames(shown)
		oneSide := 0
		for _, name := range names {
			// --sync sans jeu : un jeu qui n'a de sauvegardes que d'un côté n'est pas une
			// différence (machine à plus petit stockage, jeu pas installé). Le copier sur
			// l'autre machine : --sync "<jeu>", explicite.
			t, terr := syncTargetOf(name)
			if game == "" {
				here := terr == nil && t.fingerprint(t.files()) != ""
				if here != (byName[name].Fingerprint != "") {
					oneSide++
					continue
				}
			}
			if errors.Is(terr, errSyncAbsent) && byName[name].Dir != "" && !dryRun {
				// Dossier déclaré sur l'autre machine, pas ici : déclaré avec son chemin, puis reçu.
				t, terr = declareFromPeer(name, byName[name].Dir)
			}
			if terr != nil {
				g.line(stSkip, labels[name], "absent ici (dossier non déclaré : --sync dir add, ou Eden absent)")
				continue
			}
			st, msg := syncGameWith(client, peer.Name, peerID, stateRoot, name, t, byName[name], dryRun)
			g.line(st, labels[name], msg)
			if st == stFail {
				failed = true
			}
		}
		if oneSide > 0 {
			uiStatusLine(stSkip, fmt.Sprintf("%d jeu(x) avec des sauvegardes d'un seul côté, non synchronisé(s) : "+
				"--sync \"<jeu>\" pour en copier un", oneSide))
		}
	}
	uiNote("Réseau local uniquement. Versions écartées en cas de conflit : " + backupDir())
	if failed {
		return 1
	}
	return 0
}

// syncErrStatus : statut d'une erreur de synchro d'un jeu : jeu en cours sur l'autre machine
// (ignoré, la synchro reprendra plus tard), sinon échec.
func syncErrStatus(err error, peerName string) (uiStatus, string) {
	if errors.Is(err, errSyncBusy) {
		return stSkip, "en cours sur " + peerName + ", ignoré"
	}
	return stFail, err.Error()
}

// archivedNote : « (chemin) » après une archive, rien sans archive.
func archivedNote(path string) string {
	if path == "" {
		return ""
	}
	return " (" + path + ")"
}

// syncGameWith : synchronise UNE cible (jeu, dossier, jeu Switch) avec une machine associée.
// remote : son état sur la machine distante (liste). dryRun : rien n'est verrouillé ni écrit.
// Retourne le statut et le texte de la ligne.
func syncGameWith(c *syncClient, peerName, peerID, stateRoot, name string, t syncTarget, remote remoteSave, dryRun bool) (uiStatus, string) {
	if t.game && gameUsesSaveDir(name) {
		return stSkip, "sauvegardes hors du prefix (SAVEDIR/SAVEFILES), non synchronisées"
	}
	var lock *os.File
	if !dryRun {
		var f *os.File
		var ok bool
		if t.game {
			f, ok = syncLockGame(name)
		} else {
			f, ok = t.lock()
		}
		if !ok {
			return stSkip, "en cours ici, ignoré"
		}
		lock = f
	}
	if lock != nil {
		defer lock.Close()
	}
	files := t.files()
	fp := t.fingerprint(files)
	if fp == "" && remote.Fingerprint == "" {
		return stSkip, "aucune sauvegarde ici ni sur " + peerName
	}
	state := loadSyncState(stateRoot, peerID, name).Fingerprint
	action := decideSync(fp, state, newestSave(files), remote.Fingerprint, remote.State, remote.Newest, t.content(files), remote.Content)
	if dryRun {
		return stInfo, action.String()
	}
	switch action {
	case syncUpToDate:
		if state != fp {
			if err := saveSyncState(stateRoot, peerID, name, fp); err != nil {
				return stFail, err.Error()
			}
			if err := c.ack(name, fp); err != nil && isUnreachable(err) {
				return syncErrStatus(err, peerName)
			}
		}
		return stOK, "à jour"

	case syncConflict:
		// Les deux ont changé : rien n'est transféré ni écrasé. La copie d'ici part au manager
		// (version de conflit), où l'utilisateur départage.
		msg := "conflit avec " + peerName + " (les deux ont changé) : à départager dans le manager"
		return stSkip, msg + sendConflictToManager(peerName, name, t)

	case syncSend:
		_, _, err := c.send(t)
		if err != nil {
			return syncErrStatus(err, peerName)
		}
		if err := saveSyncState(stateRoot, peerID, name, t.fingerprint(t.files())); err != nil {
			return stFail, err.Error()
		}
		return stOK, "envoyé à " + peerName

	case syncReceive:
		// Version d'ici modifiée depuis la dernière synchro (ou état inconnu, première synchro) :
		// mise de côté avant d'être remplacée.
		archivedLocal := ""
		if fp != "" && fp != state {
			path, err := t.archiveLocal()
			if err != nil {
				return stFail, "version d'ici non archivée, rien reçu : " + err.Error()
			}
			archivedLocal = path
		}
		rfp, err := c.fetch(t)
		if err != nil {
			return syncErrStatus(err, peerName)
		}
		if err := c.ack(name, rfp); err != nil && isUnreachable(err) {
			return stFail, "reçu, mais l'état n'est pas confirmé sur " + peerName + " : " + err.Error()
		}
		if err := saveSyncState(stateRoot, peerID, name, t.fingerprint(t.files())); err != nil {
			return stFail, err.Error()
		}
		return stOK, "reçu de " + peerName + archivedNote(archivedLocal)
	}
	return stFail, "action de synchro inconnue"
}
