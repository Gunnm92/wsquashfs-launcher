package main

// Synchro automatique à la sortie d'un jeu (--sync auto on, sync_cmd.go) : la partie est envoyée au
// manager (wsquashfs-manager, déclaré par PUT /v1/saves/manager) quand il est joignable. Jamais de
// réception, jamais de verrou (le jeu tient le sien), jamais d'arrêt : une erreur est signalée, le
// code de sortie du jeu ne change pas. Le manager garde la version qu'il remplace (archive).

import "time"

// autoSyncConnectTimeout : délai de connexion au manager à la sortie d'un jeu : court, la sortie
// n'attend pas un manager éteint. Variable pour les tests.
var autoSyncConnectTimeout = 2 * time.Second

// autoSyncAfterGame : à la sortie d'un jeu, envoie sa partie au manager si la synchro automatique est
// active, qu'un manager est déclaré, et que la partie a changé depuis le dernier envoi.
func autoSyncAfterGame(name string) {
	cfg := loadSyncConfig()
	peer, ok := managerPeer(cfg)
	if !cfg.Auto || !ok || !validSyncGame(name) {
		return
	}
	// Sauvegardes hors du prefix (SAVEDIR/SAVEFILES, lus au lancement) : ni copiées ni synchronisées.
	// Pas de gameUsesSaveDir ici : il réécrit l'état du jeu en cours (image, couches) avant le
	// démontage.
	if launch.savedirKey != "" || launch.savefilesKey != "" {
		return
	}
	t := gameTarget(name, syncUsersDir(name))
	fp := t.fingerprint(t.files())
	if fp == "" {
		return
	}
	peerID := syncPeerID(peer)
	if fp == loadSyncState(syncStateRoot(), peerID, name).Fingerprint {
		return // déjà envoyée
	}
	client := newSyncClientDial(peer, cfg.ID, autoSyncConnectTimeout)
	_, archived, err := client.send(t)
	if err != nil {
		if isUnreachable(err) {
			uiNote("Manager injoignable : partie gardée ici (wsquashfs-launcher --sync plus tard)")
			return
		}
		uiStatusLine(stFail, "envoi au manager : "+err.Error())
		return
	}
	if err := saveSyncState(syncStateRoot(), peerID, name, t.fingerprint(t.files())); err != nil {
		uiStatusLine(stFail, "état de synchro non enregistré : "+err.Error())
		return
	}
	uiStatusLine(stOK, "Partie envoyée au manager"+archivedNote(archived))
}
