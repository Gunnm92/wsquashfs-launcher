// wsquashfs-launcher : lance un jeu Windows .wsquashfs (format Batocera) sur
// n'importe quel Linux. Réécriture en Go du script bash, sans dépendance
// tierce. Le script bash reste la référence tant que la parité n'est pas
// atteinte (tests/run-tests.sh).
package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/signal"
	"strconv"
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
	installSignals()
	cleanupHook = cleanup

	fixOwnership(cacheDir)
	fixOwnership(savesDir)

	switch action {
	case "clean", "clear", "umount", "pack", "install", "info", "show", "saves",
		"list", "doctor", "agent", "pair", "update":
		return maintenanceMain()
	}
	launchGame()
	return 0
}

// installSignals : SIGINT / SIGTERM passent par cleanup. Pendant un namespace
// de l'overlay noyau, le signal est transmis au lanceur du namespace, qui
// arrête Wine et démonte (le retour de l'hôte fait le nettoyage).
// signalExiting : un signal arrête le lanceur ; le thread principal se bloque.
var signalExiting bool

func installSignals() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-ch
		if nsPID != 0 {
			_ = syscall.Kill(nsPID, syscall.SIGTERM)
			return
		}
		code := 128 + 15
		if sig == syscall.SIGINT {
			code = 128 + 2
		}
		// Le thread principal ne doit plus rien faire : bash quittait sur le trap.
		signalExiting = true
		exitNow(code)
	}()
}

// cleanup : fin du lanceur (normale, erreur ou signal) : arrêt de ATTACH, du
// pont DualSense, de Wine, puis démontage (copie et dossier non concernés).
func cleanup() {
	_ = os.Chdir("/")
	stopAttachCommand()
	stopDualsenseBridge()
	if mounted {
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
