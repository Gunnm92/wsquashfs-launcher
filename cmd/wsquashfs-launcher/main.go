// wsquashfs-launcher : lance un jeu Windows .wsquashfs (format Batocera) sur
// n'importe quel Linux. Binaire Go statique (seule dépendance : lipgloss, pour l'affichage), issu de la
// réécriture du script bash ; tests/run-tests.sh en vérifie le comportement.
package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"

	"wsquashfs-launcher/internal/setup"
	"wsquashfs-launcher/internal/ui"
)

//go:embed usage.txt
var usageText string

func main() {
	os.Exit(run(os.Args[1:]))
}

// run : point d'entrée (section 10 du script bash).
func run(args []string) int {
	launch.origArgs = append([]string{}, args...)
	if len(args) > 0 && args[0] == "--complete" { // complétion bash (completion.go) : rien d'autre
		return cmdComplete(args[1:])
	}
	if len(args) > 0 && args[0] == "--serve-image" { // serveur FUSE d'une image (imagefs.go), interne
		return serveImage(args[1:])
	}
	opts, err := parseArgs(args)
	if err != nil {
		var ae *argError
		if errors.As(err, &ae) && ae.usage {
			ui.Usage(ae.msg)
		} else {
			ui.Error(err.Error())
		}
		return 1
	}
	switch opts.action {
	case "help":
		ui.KV(0, ui.Pair{"Version", launcherBuild()})
		ui.Out("")
		ui.Help(usageText)
		return 0
	case "setup":
		return setup.Run(opts.cmdArgs, setup.Env{
			AgentDir:         agentSrv().Dir(),
			TrayAutostart:    trayAutostartPath(),
			CoverPath:        imageCoverPath,
			CompletionScript: completionScript,
			RemoveAgentUnits: agentSrv().RemoveUnits,
		})
	}
	applyOptions(opts)
	return launcherMain()
}

// applyOptions : options de la ligne de commande dans l'état du lancement.
func applyOptions(o *options) {
	launch.action = o.action
	launch.gameArg = o.gameArg
	launch.wineOverride = o.wineOver
	launch.setArgs = o.setArgs
	launch.headless = o.headless
	launch.execCmd = o.execCmd
	launch.cmdArgs = o.cmdArgs
}

// launcherMain : enchaînement principal (main de la section 10).
func launcherMain() int {
	restoreSudoUserSession()
	limitStackSize()
	raiseNofileLimit()
	initConfig()

	// Relancé dans le namespace de l'overlay noyau : variables d'échange.
	if stage := os.Getenv("WSQUASHFS_NS_STAGE"); stage != "" {
		launch.nsStage = stage
		launch.nsDir = os.Getenv("WSQUASHFS_NS_DIR")
		if fd, err := strconv.Atoi(os.Getenv("WSQUASHFS_NS_LOCK_FD")); err == nil && fd >= 0 {
			if _, err := os.Stat(fmt.Sprintf("/proc/self/fd/%d", fd)); err == nil {
				launch.lockFile = os.NewFile(uintptr(fd), "lock")
			}
		}
		os.Unsetenv("WSQUASHFS_NS_STAGE")
		os.Unsetenv("WSQUASHFS_NS_DIR")
		os.Unsetenv("WSQUASHFS_NS_LOCK_FD")
	}
	// cleanupHook avant le gestionnaire : la goroutine le lit sans verrou.
	cleanupHook = cleanup
	// --trial gère lui-même SIGTERM (arrêt du jeu qu'il a lancé, puis rapport).
	if launch.action != "trial" {
		installSignals()
	}

	fixOwnership(conf.cacheDir)
	fixOwnership(conf.savesDir)

	switch launch.action {
	case "clean", "clear", "erase", "umount", "pack", "install", "info", "show", "save", "saves",
		"list", "doctor", "agent", "pair", "sync", "tray", "trial", "update":
		return maintenanceMain()
	}
	launchGame()
	return 0
}

// installSignals : SIGINT / SIGTERM passent par cleanup. Pendant un namespace
// de l'overlay noyau, le signal est transmis au lanceur du namespace, qui
// arrête Wine et démonte (le retour de l'hôte fait le nettoyage) ; chaque
// signal suivant est transmis de même.
// signalExiting : un signal arrête le lanceur ; le thread principal se bloque.
var signalExiting atomic.Bool

// signalGate : tenu par le thread principal pendant un montage ; le
// gestionnaire de signaux le prend avant cleanup, qui voit ainsi un état
// complet (montage fini et mounted à jour), jamais un montage à moitié fait.
var signalGate sync.Mutex

func installSignals() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for sig := range ch {
			if pid := nsPID.Load(); pid != 0 {
				_ = syscall.Kill(int(pid), syscall.SIGTERM)
				continue
			}
			// --pair attend l'association : annulation propre (son code rend 130) ;
			// l'agent sert : arrêt propre, requêtes en cours finies (code 0).
			if hook := interruptHook.Load(); hook != nil {
				(*hook)()
				continue
			}
			code := 128 + 15
			if sig == syscall.SIGINT {
				code = 128 + 2
			}
			// Le thread principal ne doit plus rien faire : bash quittait sur le trap.
			signalExiting.Store(true)
			signalGate.Lock() // jamais rendu : la sortie suit
			exitNow(code)
		}
	}()
}

// cleanup : fin du lanceur (normale, erreur ou signal) : arrêt de ATTACH, du
// pont DualSense, de Wine, puis démontage (copie et dossier non concernés).
// Ne doit jamais appeler exitNow ni die : exitNow l'exécute sous sync.Once,
// un appel imbriqué bloquerait la sortie.
func cleanup() {
	_ = os.Chdir("/")
	stopAttachCommand()
	stopDualsenseBridge()
	// --clean interrompu pendant la recherche de doublons : image démontée, dossier retiré.
	if p := purgeMount.Load(); p != nil {
		umountRetry(*p)
		_ = os.Remove(*p)
	}
	if mounted.Load() {
		stopWineserver()
		if launch.mountMode == "overlay" {
			umountRetry(launch.winePoint)
			umountRetry(launch.squashfsPoint)
			if st, err := os.Stat(launch.workPoint); err == nil && st.IsDir() {
				resetWorkdir(launch.workPoint)
				_ = os.Remove(launch.workPoint)
			}
		}
	}
}
