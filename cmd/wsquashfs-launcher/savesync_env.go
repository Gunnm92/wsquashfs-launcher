package main

// Environnement de la synchro des sauvegardes (internal/savesync) : ce que le paquet reçoit du lanceur.

import (
	"wsquashfs-launcher/internal/savesync"
)

// syncer : la synchro des sauvegardes, avec les dossiers et les fonctions du lanceur du moment.
func syncer() *savesync.Syncer {
	return savesync.New(syncEnv())
}

// autoSyncAfterGame : à la sortie d'un jeu, envoi de sa partie au manager (--sync auto). Un jeu
// qui déclare SAVEDIR/SAVEFILES (lus au lancement) garde ses sauvegardes hors du prefix : rien.
func autoSyncAfterGame(name string) {
	syncer().AutoAfterGame(name, launch.savedirKey != "" || launch.savefilesKey != "")
}

// syncEnv : ce que savesync reçoit du lanceur.
func syncEnv() savesync.Env {
	return savesync.Env{
		CacheDir:        conf.cacheDir,
		SavesDir:        conf.savesDir,
		LockIdleGame:    lockIdleGame,
		GameUsesSaveDir: gameUsesSaveDir,
		ArchiveUsers:    archiveUsers,
		CopyUsersStash:  copyUsersStash,
		BackupDir:       backupDir,
		AgentRunning: func() bool {
			_, running := runningAgentPid()
			return running
		},
		CertFingerprint: certFingerprint,
		AgentRoms:       func() []string { return loadAgentConfig().Roms },
		MaxBody:         syncMaxBody,
	}
}
