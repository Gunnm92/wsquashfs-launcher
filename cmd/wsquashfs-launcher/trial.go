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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// trialReport : résultat d'un essai (JSON sur la sortie standard).
type trialReport struct {
	Error         string     `json:"error,omitempty"` // busy | noimage
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
// préfixes en conflit). L'agent et l'essai lui-même ne comptent pas.
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
		if strings.HasPrefix("wsquashfs-launcher", comm) && comm != "" &&
			!strings.Contains(processCmdline(pid), "--agent") {
			return true
		}
	}
	return false
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

// trialWindows : fenêtres des processus du jeu (xprop, si disponible).
func trialWindows(pids map[int]bool) []trialWin {
	if _, err := exec.LookPath("xprop"); err != nil || os.Getenv("DISPLAY") == "" {
		return nil
	}
	var out []trialWin
	list, _ := exec.Command("xprop", "-root", "_NET_CLIENT_LIST").Output()
	for _, id := range regexp.MustCompile(`0x[0-9a-f]+`).FindAllString(string(list), -1) {
		pidOut, _ := exec.Command("xprop", "-id", id, "_NET_WM_PID").Output()
		m := regexp.MustCompile(`\d+$`).FindString(strings.TrimSpace(string(pidOut)))
		pid, _ := strconv.Atoi(m)
		if !pids[pid] {
			continue
		}
		nameOut, _ := exec.Command("xprop", "-id", id, "WM_NAME").Output()
		name := regexp.MustCompile(`^WM_NAME\([^)]*\) = "(.*)"$`).FindStringSubmatch(strings.TrimSpace(string(nameOut)))
		state, _ := exec.Command("xprop", "-id", id, "_NET_WM_STATE").Output()
		w := trialWin{ID: id, State: strings.TrimPrefix(strings.TrimSpace(string(state)), "_NET_WM_STATE: ")}
		if name != nil {
			w.Name = name[1]
		}
		out = append(out, w)
	}
	return out
}

// runTrial : lance l'image avec ce binaire, observe, arrête, et écrit le rapport.
// Retourne le code de sortie du processus (0, 3 jeu en cours, 4 image absente).
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

	cmd := exec.Command(selfPath, image)
	cmd.Env = importSessionEnv(session)
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
	// Observation : toutes les 3 s, jusqu'à la durée demandée ou la fin du lanceur.
	for time.Since(start) < time.Duration(seconds)*time.Second && launcherAlive() {
		time.Sleep(3 * time.Second)
		if len(gamePIDs(stem)) > 0 {
			report.GameSeen = true
		}
	}
	report.Elapsed = int(time.Since(start).Seconds())
	report.LauncherAlive = launcherAlive()

	for _, pid := range gamePIDs(stem) {
		if len(report.Games) < 3 {
			report.Games = append(report.Games, processCmdline(pid))
		}
	}
	pids := map[int]bool{}
	for _, pid := range prefixPIDs(stem) {
		pids[pid] = true
	}
	report.Windows = append(report.Windows, trialWindows(pids)...)

	if shot != "" && os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("grim"); err == nil {
			if exec.Command("grim", shot).Run() == nil {
				report.Screenshot = true
			}
		}
	}

	// Arrêt : TERM aux processus du préfixe, puis KILL après 5 s, puis le lanceur.
	for _, pid := range prefixPIDs(stem) {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	time.Sleep(5 * time.Second)
	for _, pid := range prefixPIDs(stem) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	for i := 0; i < 30 && launcherAlive(); i++ {
		time.Sleep(time.Second)
	}
	if launcherAlive() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	report.Left.Procs = len(prefixPIDs(stem))
	report.Left.Mounts = countMounts(stem)

	if st := cmd.ProcessState; st != nil {
		report.Code = st.ExitCode()
	} else {
		report.Code = -1
	}
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
