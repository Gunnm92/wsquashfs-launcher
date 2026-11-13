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

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const syncUsage = "wsquashfs-launcher --sync [jeu | status [jeu] | forget <machine> | wsq://…]"

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

// syncGameArg : nom de jeu d'un argument (image, dossier ou nom) ; sortie si le nom n'est pas valide.
func syncGameArg(arg string) string {
	game := gameNameOf(arg)
	if !validSyncGame(game) {
		die("jeu invalide : " + arg)
	}
	return game
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

// syncPair : associe la machine dont le code (wsq://…) est donné, ou remplace son association.
func syncPair(cfg syncConfig, code string) int {
	peer, err := parsePairCode(code)
	if err != nil {
		die(err.Error())
	}
	uiTitle("Association de machine", "réseau local")
	if fp, err := certFingerprint(); err == nil && fp == peer.Fingerprint {
		die("c'est cette machine : coller le code affiché par --pair sur l'AUTRE machine")
	}
	host, err := newSyncClient(peer, cfg.ID).info()
	if err != nil {
		if isUnreachable(err) {
			die(peer.URL + " injoignable : l'autre machine est-elle allumée, sur le même réseau, et son agent lancé (--agent) ?")
		}
		die(err.Error())
	}
	peer.Name = host
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
		if g, ok := strings.CutSuffix(e.Name(), ".json"); ok && validSyncGame(g) {
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
	for _, g := range syncGames() {
		local[g] = true
	}
	peers := append([]syncPeer{}, cfg.Peers...)
	sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
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
			if validSyncGame(r.Name) {
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
		sort.Strings(names)
		if game != "" {
			if !games[game] {
				newUIGames([]string{game}).line(stSkip, game, "aucune sauvegarde ici ni sur "+peer.Name)
				continue
			}
			names = []string{game}
		}
		g := newUIGames(names)
		for _, name := range names {
			st, msg := syncGameWith(client, peer.Name, peerID, stateRoot, name, syncUsersDir(name), byName[name], dryRun)
			g.line(st, name, msg)
			if st == stFail {
				failed = true
			}
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

// conflictWhy : ce qui a départagé un conflit gagné (« plus lourde », ou « plus récente » à taille
// égale) et les deux tailles, gagnant d'abord.
func conflictWhy(winSize, loseSize int64) string {
	why := "plus récente"
	if winSize != loseSize {
		why = "plus lourde"
	}
	return why + " gardée (" + sizeText(winSize) + " contre " + sizeText(loseSize) + ")"
}

// syncGameWith : synchronise UN jeu avec une machine associée. users : users/ local du jeu ; remote :
// son état sur la machine distante (liste). dryRun : rien n'est verrouillé ni écrit. Retourne le
// statut et le texte de la ligne du jeu.
func syncGameWith(c *syncClient, peerName, peerID, stateRoot, name, users string, remote remoteSave, dryRun bool) (uiStatus, string) {
	if gameUsesSaveDir(name) {
		return stSkip, "sauvegardes hors du prefix (SAVEDIR/SAVEFILES), non synchronisées"
	}
	var lock *os.File
	if !dryRun {
		f, ok := syncLockGame(name)
		if !ok {
			return stSkip, "en cours ici, ignoré"
		}
		lock = f
	}
	if lock != nil {
		defer lock.Close()
	}
	files := collectSaveFiles(users)
	fp := saveFingerprint(users, files)
	if fp == "" && remote.Fingerprint == "" {
		return stSkip, "aucune sauvegarde ici ni sur " + peerName
	}
	state := loadSyncState(stateRoot, peerID, name).Fingerprint
	localSize := saveSize(files)
	action := decideSync(fp, state, newestSave(files), localSize, remote.Fingerprint, remote.State, remote.Newest, remote.Size)
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

	case syncSend, syncConflictSend:
		_, archived, err := c.send(name, users)
		if err != nil {
			return syncErrStatus(err, peerName)
		}
		if err := saveSyncState(stateRoot, peerID, name, saveFingerprint(users, collectSaveFiles(users))); err != nil {
			return stFail, err.Error()
		}
		if action == syncConflictSend {
			return stOK, "conflit : version d'ici " + conflictWhy(localSize, remote.Size) + " ; celle de " + peerName + " archivée là-bas" + archivedNote(archived)
		}
		return stOK, "envoyé à " + peerName

	case syncReceive, syncConflictReceive:
		// Version d'ici modifiée depuis la dernière synchro, ou perdante d'un conflit (même quand
		// l'état enregistré est incohérent) : mise de côté avant d'être remplacée.
		archivedLocal := ""
		if fp != "" && (fp != state || action == syncConflictReceive) {
			path, err := archiveUsers(name, filepath.Dir(users), filepath.Base(users))
			if err != nil {
				return stFail, "version d'ici non archivée, rien reçu : " + err.Error()
			}
			archivedLocal = path
		}
		rfp, err := c.fetch(name, users)
		if err != nil {
			return syncErrStatus(err, peerName)
		}
		if err := c.ack(name, rfp); err != nil && isUnreachable(err) {
			return stFail, "reçu, mais l'état n'est pas confirmé sur " + peerName + " : " + err.Error()
		}
		if err := saveSyncState(stateRoot, peerID, name, saveFingerprint(users, collectSaveFiles(users))); err != nil {
			return stFail, err.Error()
		}
		if action == syncConflictReceive {
			return stOK, "conflit : version de " + peerName + " " + conflictWhy(remote.Size, localSize) + " ; celle d'ici archivée dans " + archivedLocal
		}
		return stOK, "reçu de " + peerName
	}
	return stFail, "action de synchro inconnue"
}
