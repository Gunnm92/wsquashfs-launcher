package main

// Environnement de l'agent (internal/agent) : ce que le paquet reçoit du lanceur.

import (
	"fmt"
	"sync/atomic"
	"time"

	"wsquashfs-launcher/internal/agent"
	"wsquashfs-launcher/internal/savesync"
)

// interruptHook : appelé à la place de la sortie quand un signal arrive (installSignals), tant que
// --pair attend l'association, que l'agent sert (arrêt propre) ou que l'icône de notification tourne.
var interruptHook atomic.Pointer[func()]

// agentStopGrace : délai laissé aux requêtes en cours à l'arrêt de l'agent ; 0 : celui du paquet. Variable pour les tests.
var agentStopGrace time.Duration

// launcherExe : ce binaire, pour lancer ses propres actions.
func launcherExe() string {
	return conf.selfPath
}

// agentSrv : l'agent, avec les fonctions du lanceur du moment.
func agentSrv() *agent.Server {
	return agent.New(agentEnv())
}

// agentEnv : ce que l'agent reçoit du lanceur.
func agentEnv() agent.Env {
	return agent.Env{
		Exe:          launcherExe,
		Syncer:       func() *savesync.Syncer { return syncer() },
		ListRunners:  listWineRunners,
		LockIdleGame: lockIdleGame,
		SetInterrupt: func(hook func()) {
			if hook == nil {
				interruptHook.Store(nil)
				return
			}
			interruptHook.Store(&hook)
		},
		MaxBody:     func() int64 { return syncMaxBody },
		StopGrace:   agentStopGrace,
		Build:       cachedBuild,
		Playing:     playingGames,
		Controllers: connectedControllers,
		Restart:     restartAgent,
	}
}

// durationText : durée lisible pour un message (« 10 min », « 300ms »).
func durationText(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return d.String()
}
