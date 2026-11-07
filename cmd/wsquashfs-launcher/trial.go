package main

// --trial (essai chronométré) : lance un jeu pendant une durée donnée, observe
// ses processus et fenêtres, puis l'arrête. Remplace le script shell qui était
// dupliqué dans l'agent et dans wsquashfs-manager ; la sortie est un rapport
// JSON unique.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// trialReport : résultat d'un essai (JSON sur la sortie standard).
type trialReport struct {
	Error         string     `json:"error,omitempty"` // busy | noimage | interrupted
	Code          int        `json:"code"`            // code de sortie du lanceur (-1 si arrêté par signal)
	Elapsed       int        `json:"elapsed"`
	LauncherAlive bool       `json:"launcher_alive"`
	GameSeen      bool       `json:"game_seen"`
	Games         []string   `json:"games"`
	Windows       []trialWin `json:"windows"`
	Left          trialLeft  `json:"left"`
	Screenshot    bool       `json:"screenshot"`
	Log           string     `json:"log"`
}

type trialWin struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type trialLeft struct {
	Procs  int `json:"procs"`
	Mounts int `json:"mounts"`
}

// trialSystemProcs : processus de Wine qui ne sont pas « le jeu ».
var trialSystemProcs = regexp.MustCompile(`(?i)^(services|winedevice|plugplay|svchost|explorer|rpcss|tabtip|xalia|umu|conhost|start|wineboot|winemenubuilder)\.exe$`)

var trialExeRe = regexp.MustCompile(`(?i)[^\\/ ]+\.exe`)

// processComm : nom du processus (comm), "" si illisible.
func processComm(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// processEnviron : variables d'un processus.
func processEnviron(pid int) []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
}

// processCmdline : ligne de commande (espaces entre les arguments).
func processCmdline(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(strings.TrimRight(string(data), "\x00"), "\x00", " "))
}

// allPIDs : tous les processus visibles.
func allPIDs() []int {
	entries, _ := filepath.Glob("/proc/[0-9]*")
	var out []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(filepath.Base(e)); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

// launcherRunning : un autre lanceur de jeu ou umu-run tourne (montages et
// préfixes en conflit). L'agent, l'essai lui-même et les actions qui ne
// lancent pas de jeu (--doctor, --list…) ne comptent pas.
func launcherRunning() bool {
	self, parent := os.Getpid(), os.Getppid()
	for _, pid := range allPIDs() {
		if pid == self || pid == parent {
			continue
		}
		comm := processComm(pid)
		if comm == "umu-run" {
			return true
		}
		if isLauncherComm(comm) && launchesGame(processArgs(pid)) {
			return true
		}
	}
	return false
}

// isLauncherComm : comm d'un wsquashfs-launcher (tronqué à 15 caractères par
// le noyau : « wsquashfs-launc »).
func isLauncherComm(comm string) bool {
	const name = "wsquashfs-launcher"
	return comm == name || (len(comm) == 15 && strings.HasPrefix(name, comm))
}

// launchesGame : la ligne de commande d'un lanceur (args sans argv[0]) lance,
// prépare ou démonte un jeu, au lieu d'une action d'information ou de
// maintenance sans montage. Ligne illisible : considérée comme un jeu.
func launchesGame(args []string) bool {
	o, err := parseArgs(args)
	if err != nil {
		return true
	}
	switch o.action {
	case "agent", "pair", "trial", "update", "setup", "help", "doctor", "list",
		"info", "show", "saves", "clean", "clear", "pack", "install":
		return false
	}
	return true
}

// processArgs : arguments d'un processus, sans argv[0].
func processArgs(pid int) []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil
	}
	args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if len(args) > 0 {
		args = args[1:]
	}
	return args
}

// prefixPIDs : processus dont WINEPREFIX est le préfixe du jeu (chemin
// …/wsquashfs/wine/<jeu>).
func prefixPIDs(stem string) []int {
	re := regexp.MustCompile("^WINEPREFIX=.*/wsquashfs/wine/" + regexp.QuoteMeta(stem) + "(/.*)?$")
	var out []int
	for _, pid := range allPIDs() {
		for _, kv := range processEnviron(pid) {
			if re.MatchString(kv) {
				out = append(out, pid)
				break
			}
		}
	}
	return out
}

// gamePIDs : processus de prefixPIDs dont la ligne de commande lance un .exe
// qui n'est pas un composant de Wine.
func gamePIDs(stem string) []int {
	var out []int
	for _, pid := range prefixPIDs(stem) {
		exe := trialExeRe.FindString(processCmdline(pid))
		if exe == "" || trialSystemProcs.MatchString(exe) {
			continue
		}
		out = append(out, pid)
	}
	return out
}

// importSessionEnv : environnement de la session graphique (DISPLAY,
// WAYLAND_DISPLAY…), lu dans le processus session_process. comm est tronqué à
// 15 caractères par le noyau.
func importSessionEnv(session string) []string {
	env := os.Environ()
	if session == "" {
		return env
	}
	short := session
	if len(short) > 15 {
		short = short[:15]
	}
	for _, pid := range allPIDs() {
		if processComm(pid) == short {
			return append(env, processEnviron(pid)...)
		}
	}
	return env
}

// envValue : valeur de key dans env (la dernière l'emporte, comme os/exec).
func envValue(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			val = kv[len(key)+1:]
		}
	}
	return val
}

// trialWindows : fenêtres des processus du jeu (xprop, si disponible), dans
// l'environnement de la session graphique (env).
func trialWindows(pids map[int]bool, env []string) []trialWin {
	if _, err := exec.LookPath("xprop"); err != nil || envValue(env, "DISPLAY") == "" {
		return nil
	}
	xprop := func(args ...string) string {
		cmd := exec.Command("xprop", args...)
		cmd.Env = env
		out, _ := cmd.Output()
		return strings.TrimSpace(string(out))
	}
	var out []trialWin
	for _, id := range regexp.MustCompile(`0x[0-9a-f]+`).FindAllString(xprop("-root", "_NET_CLIENT_LIST"), -1) {
		m := regexp.MustCompile(`\d+$`).FindString(xprop("-id", id, "_NET_WM_PID"))
		pid, _ := strconv.Atoi(m)
		if !pids[pid] {
			continue
		}
		name := regexp.MustCompile(`^WM_NAME\([^)]*\) = "(.*)"$`).FindStringSubmatch(xprop("-id", id, "WM_NAME"))
		w := trialWin{ID: id, State: strings.TrimPrefix(xprop("-id", id, "_NET_WM_STATE"), "_NET_WM_STATE: ")}
		if name != nil {
			w.Name = name[1]
		}
		out = append(out, w)
	}
	return out
}

// trialScreenshot : capture Wayland (grim) dans l'environnement de la session.
func trialScreenshot(shot string, env []string) bool {
	if shot == "" || envValue(env, "WAYLAND_DISPLAY") == "" {
		return false
	}
	if _, err := exec.LookPath("grim"); err != nil {
		return false
	}
	cmd := exec.Command("grim", shot)
	cmd.Env = env
	return cmd.Run() == nil
}

// runTrial : lance l'image avec ce binaire, observe, arrête, et écrit le rapport.
// Retourne le code de sortie du processus (0, 3 jeu en cours, 4 image absente).
// SIGTERM ou SIGINT (agent interrompu) : arrêt immédiat du jeu, puis rapport.
func runTrial(image string, seconds int, session, shot string) int {
	report := trialReport{Games: []string{}, Windows: []trialWin{}}
	emit := func() int {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
		switch report.Error {
		case "busy":
			return 3
		case "noimage":
			return 4
		}
		return 0
	}
	if launcherRunning() {
		report.Error = "busy"
		return emit()
	}
	if !isFile(image) {
		report.Error = "noimage"
		return emit()
	}
	stem := gameNameOf(image)

	out, err := os.CreateTemp("", "wsfs-trial.*.out")
	if err != nil {
		report.Error = "output"
		return emit()
	}
	defer os.Remove(out.Name())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(stop)

	env := importSessionEnv(session)
	cmd := exec.Command(selfPath, image)
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		report.Error = "launch"
		return emit()
	}
	// Fermé à la fin du lanceur : lisible à volonté, contrairement à un envoi.
	// Après sa fermeture, waitErr et cmd.ProcessState sont lisibles sans course.
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	launcherAlive := func() bool {
		select {
		case <-exited:
			return false
		default:
			return true
		}
	}
	// Observation : toutes les 3 s, jusqu'à la durée demandée, la fin du
	// lanceur ou une interruption.
	interrupted := false
	deadline := start.Add(time.Duration(seconds) * time.Second)
observe:
	for time.Now().Before(deadline) && launcherAlive() {
		select {
		case <-stop:
			interrupted = true
			break observe
		case <-exited:
		case <-time.After(3 * time.Second):
		}
		if len(gamePIDs(stem)) > 0 {
			report.GameSeen = true
		}
	}
	report.Elapsed = int(time.Since(start).Seconds())
	report.LauncherAlive = launcherAlive()

	if interrupted {
		report.Error = "interrupted"
	} else {
		for _, pid := range gamePIDs(stem) {
			if len(report.Games) < 3 {
				report.Games = append(report.Games, processCmdline(pid))
			}
		}
		pids := map[int]bool{}
		for _, pid := range prefixPIDs(stem) {
			pids[pid] = true
		}
		report.Windows = append(report.Windows, trialWindows(pids, env)...)
		report.Screenshot = trialScreenshot(shot, env)
	}

	// Arrêt : TERM aux processus du préfixe, puis KILL après 5 s, puis le
	// lanceur (TERM : il arrête Wine et démonte), attendu jusqu'à sa fin.
	for _, pid := range prefixPIDs(stem) {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
	}
	for _, pid := range prefixPIDs(stem) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	// Interrompu : l'agent n'attend pas, le lanceur est arrêté tout de suite.
	if !interrupted {
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
		}
	}
	if launcherAlive() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	}
	report.Left.Procs = len(prefixPIDs(stem))
	report.Left.Mounts = countMounts(stem)
	report.Code = cmd.ProcessState.ExitCode() // -1 si arrêté par un signal

	if data, err := os.ReadFile(out.Name()); err == nil {
		if len(data) > 60000 {
			data = data[len(data)-60000:]
		}
		report.Log = string(data)
	}
	return emit()
}

// countMounts : montages wsquashfs du jeu (mountinfo).
func countMounts(stem string) int {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return 0
	}
	re := regexp.MustCompile("/wsquashfs/.*/" + regexp.QuoteMeta(stem))
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if re.MatchString(line) {
			n++
		}
	}
	return n
}

// cmdTrialMain : --trial <image> [durée] [--session NOM] [--screenshot FICHIER].
func cmdTrialMain(args []string) int {
	if len(args) == 0 {
		usageError(os.Args[0] + " --trial <jeu.wsquashfs> [durée] [--session NOM] [--screenshot FICHIER]")
	}
	image := args[0]
	seconds := 60
	session := loadAgentConfig().Session
	shot := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--session":
			session = argAt(args, i+1)
			i++
		case "--screenshot":
			shot = argAt(args, i+1)
			i++
		default:
			n, err := strconv.Atoi(args[i])
			if err != nil {
				usageError(os.Args[0] + " --trial <jeu.wsquashfs> [durée] [--session NOM] [--screenshot FICHIER]")
			}
			seconds = n
		}
	}
	return runTrial(image, seconds, session, shot)
}
