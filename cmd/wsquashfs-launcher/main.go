// wsquashfs-launcher : lance un jeu Windows .wsquashfs (format Batocera) sur
// n'importe quel Linux. Binaire Go sans dépendance tierce, issu de la
// réécriture du script bash ; tests/run-tests.sh en vérifie le comportement.
package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
)

//go:embed usage.txt
var usageText string

func main() {
	os.Exit(run(os.Args[1:]))
}

// run : point d'entrée (section 10 du script bash).
func run(args []string) int {
	origArgs = append([]string{}, args...)
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch opts.action {
	case "help":
		fmt.Print(usageText)
		return 0
	case "setup":
		return runSetup(opts.cmdArgs)
	}
	applyOptions(opts)
	return launcherMain()
}

// applyOptions : options de la ligne de commande dans l'état du lancement.
func applyOptions(o *options) {
	action = o.action
	gameArg = o.gameArg
	wineOverride = o.wineOver
	setArgs = o.setArgs
	headless = o.headless
	execCmd = o.execCmd
	cmdArgs = o.cmdArgs
}

// launcherMain : enchaînement principal (main de la section 10).
func launcherMain() int {
	restoreSudoUserSession()
	limitStackSize()
	raiseNofileLimit()
	initConfig()

	// Relancé dans le namespace de l'overlay noyau : variables d'échange.
	if stage := os.Getenv("WSQUASHFS_NS_STAGE"); stage != "" {
		nsStage = stage
		nsDir = os.Getenv("WSQUASHFS_NS_DIR")
		if fd, err := strconv.Atoi(os.Getenv("WSQUASHFS_NS_LOCK_FD")); err == nil && fd >= 0 {
			if _, err := os.Stat(fmt.Sprintf("/proc/self/fd/%d", fd)); err == nil {
				lockFile = os.NewFile(uintptr(fd), "lock")
			}
		}
		os.Unsetenv("WSQUASHFS_NS_STAGE")
		os.Unsetenv("WSQUASHFS_NS_DIR")
		os.Unsetenv("WSQUASHFS_NS_LOCK_FD")
	}
	// cleanupHook avant le gestionnaire : la goroutine le lit sans verrou.
	cleanupHook = cleanup
	// --trial gère lui-même SIGTERM (arrêt du jeu qu'il a lancé, puis rapport).
	if action != "trial" {
		installSignals()
	}

	fixOwnership(cacheDir)
	fixOwnership(savesDir)

	switch action {
	case "clean", "clear", "umount", "pack", "install", "info", "show", "saves",
		"list", "doctor", "agent", "pair", "trial", "update":
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
	if mounted.Load() {
		stopWineserver()
		if mountMode == "overlay" {
			umountRetry(winePoint)
			umountRetry(squashfsPoint)
			if st, err := os.Stat(workPoint); err == nil && st.IsDir() {
				resetWorkdir(workPoint)
				_ = os.Remove(workPoint)
			}
		}
	}
}
