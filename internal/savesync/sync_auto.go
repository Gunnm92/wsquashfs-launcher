package savesync

// Synchro automatique à la sortie d'un jeu (--sync auto on, sync_cmd.go) : la partie est envoyée au
// manager (wsquashfs-manager, déclaré par PUT /v1/saves/manager) quand il est joignable. Jamais de
// réception, jamais de verrou (le jeu tient le sien), jamais d'arrêt : une erreur est signalée, le
// code de sortie du jeu ne change pas. Le manager garde la version qu'il remplace (archive).

import (
	"time"

	"wsquashfs-launcher/internal/ui"
)

// defaultAutoDialTimeout : délai de connexion au manager à la sortie d'un jeu (Env.AutoDialTimeout) :
// court, la sortie n'attend pas un manager éteint.
const defaultAutoDialTimeout = 2 * time.Second

// autoDialTimeout : délai de connexion au manager à la sortie d'un jeu.
func (s *Syncer) autoDialTimeout() time.Duration {
	return orDuration(s.env.AutoDialTimeout, defaultAutoDialTimeout)
}

// AutoAfterGame : à la sortie d'un jeu, envoie sa partie au manager si la synchro automatique est
// active, qu'un manager est déclaré, et que la partie a changé depuis le dernier envoi. outsidePrefix :
// le jeu garde ses sauvegardes hors du prefix (SAVEDIR/SAVEFILES, lus au lancement) : ni copiées ni
// synchronisées. Pas de GameUsesSaveDir ici : il réécrit l'état du jeu en cours (image, couches)
// avant le démontage ; l'appelant passe ce qu'il sait déjà.
func (s *Syncer) AutoAfterGame(name string, outsidePrefix bool) {
	cfg := LoadConfig()
	peer, ok := ManagerPeer(cfg)
	if !cfg.Auto || !ok || !ValidGame(name) {
		return
	}
	if outsidePrefix {
		return
	}
	t := s.GameTarget(name, s.UsersDir(name))
	fp := t.Fingerprint(t.Files())
	if fp == "" {
		return
	}
	peerID := PeerID(peer)
	if fp == LoadState(s.StateRoot(), peerID, name).Fingerprint {
		return // déjà envoyée
	}
	client := s.NewClientDial(peer, cfg.ID, s.autoDialTimeout())
	_, archived, err := client.Send(t)
	if err != nil {
		if IsUnreachable(err) {
			ui.Note("Manager injoignable : partie gardée ici (wsquashfs-launcher --sync plus tard)")
			return
		}
		ui.StatusLine(ui.Fail, "envoi au manager : "+err.Error())
		return
	}
	if err := SaveState(s.StateRoot(), peerID, name, t.Fingerprint(t.Files())); err != nil {
		ui.StatusLine(ui.Fail, "état de synchro non enregistré : "+err.Error())
		return
	}
	ui.StatusLine(ui.OK, "Partie envoyée au manager"+archivedNote(archived))
}

// sendConflictToManager : en conflit de synchro avec la machine peerName, envoie la copie d'ici au
// manager déclaré (même envoi que AutoAfterGame) : il la range comme version de conflit sans
// rien remplacer. Aucun état de synchro n'est enregistré. Retourne le complément de la ligne de
// statut ("" sans manager, ou si le manager est la machine en conflit) ; un échec est signalé, sans
// faire échouer la synchro des autres jeux.
func (s *Syncer) sendConflictToManager(peerName, name string, t Target) string {
	cfg := LoadConfig()
	mgr, ok := ManagerPeer(cfg)
	if !ok || mgr.Name == peerName {
		return ""
	}
	client := s.NewClientDial(mgr, cfg.ID, s.autoDialTimeout())
	if _, _, err := client.Send(t); err != nil {
		if IsUnreachable(err) {
			return " ; manager injoignable, copie d'ici non envoyée"
		}
		return " ; envoi de la copie d'ici au manager : " + err.Error()
	}
	return " ; copie d'ici envoyée au manager"
}
