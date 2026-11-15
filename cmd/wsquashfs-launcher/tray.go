package main

// Commande --tray : icône de zone de notification (StatusNotifierItem et dbusmenu, en pur Go par
// D-Bus via fyne.io/systray). Le menu lance les actions du lanceur en sous-processus (agent,
// synchro, association, diagnostic, ménage, mise à jour) et affiche leur résultat en notification.
// L'état affiché vient de trayState, fonction pure ; le côté bureau (notifications, presse-papiers,
// terminal, autostart) est dans tray_desktop.go.
//
//   --tray            l'icône (une seule à la fois : tray.pid, dans le dossier de l'agent)
//   --tray on | off   lancement à l'ouverture de session (~/.config/autostart)

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"fyne.io/systray"
)

//go:embed tray_icon.png
var trayIcon []byte

const trayUsage = "wsquashfs-launcher --tray  |  --tray on | off"

const (
	trayMaxGames    = 40 // jeux proposés dans le menu (emplacements créés d'avance)
	trayMaxMachines = 16 // machines proposées par « Oublier »
	trayRefresh     = 10 * time.Second
)

// pairCodeRe : code d'association dans la sortie de --pair.
var pairCodeRe = regexp.MustCompile(`wsq://\S+`)

// trayPidFile : PID de l'icône en marche, dans le dossier de l'agent.
func trayPidFile() string {
	return filepath.Join(agentDir(), "tray.pid")
}

// trayRunning : PID inscrit dans trayPidFile, s'il est bien une icône.
func trayRunning() (int, bool) {
	data, err := os.ReadFile(trayPidFile())
	if err != nil {
		return 0, false
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid, pid > 0 && isProcessWithArg(pid, "--tray")
}

// isProcessWithArg : le processus pid a arg parmi ses arguments (ligne de commande de /proc).
func isProcessWithArg(pid int, arg string) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	for _, a := range strings.Split(strings.TrimRight(string(data), "\x00"), "\x00") {
		if a == arg {
			return true
		}
	}
	return false
}

// trayView : ce que le menu affiche, calculé sans D-Bus ni fichier (trayState).
type trayView struct {
	AgentRunning bool
	AutoSync     bool
	ManagerURL   string   // interface du manager, "" sans manager ou sans adresse donnée
	Machines     []string // machines associées à l'agent, au plus trayMaxMachines
	Games        []string // jeux connus, au plus trayMaxGames
	GamesMore    int      // jeux non proposés
}

// trayState : état affiché à partir de valeurs déjà lues (agent, sync.json, machines de l'agent,
// jeux connus). Ne lit rien, pour être testée seule.
func trayState(cfg syncConfig, agentRunning bool, machines, games []string) trayView {
	v := trayView{AgentRunning: agentRunning, AutoSync: cfg.Auto}
	if p, ok := managerPeer(cfg); ok {
		v.ManagerURL = p.UI
	}
	v.Machines = machines[:min(len(machines), trayMaxMachines)]
	v.Games = games[:min(len(games), trayMaxGames)]
	v.GamesMore = len(games) - len(v.Games)
	return v
}

// lastLaunch : date du dernier lancement d'un jeu (savesDir/.images/<jeu>, réécrit à chaque
// lancement par prepareMountDirs) ; zéro si inconnue.
func lastLaunch(name string) time.Time {
	if fi, err := os.Stat(filepath.Join(savesDir, ".images", name)); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// byLastLaunch : jeux du plus récemment lancé au plus ancien, puis les jamais lancés par nom (le
// menu n'en montre que trayMaxGames).
func byLastLaunch(games []string, when func(string) time.Time) []string {
	out := append([]string(nil), games...)
	at := make(map[string]time.Time, len(out))
	for _, g := range out {
		at[g] = when(g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !at[out[i]].Equal(at[out[j]]) {
			return at[out[i]].After(at[out[j]])
		}
		return out[i] < out[j]
	})
	return out
}

// trayGameSlot : un jeu du sous-menu (emplacement fixe, affiché ou caché).
type trayGameSlot struct {
	menu   *systray.MenuItem
	launch *systray.MenuItem
	save   *systray.MenuItem
}

// trayApp : le menu et son état. Les clics arrivent chacun dans leur goroutine ; mu protège les
// libellés et les listes lues par les clics ; busy : une seule action longue à la fois.
type trayApp struct {
	mu       sync.Mutex
	busy     atomic.Bool // une action longue tourne
	pairing  atomic.Bool // --pair attend une association (n'empêche pas les autres actions)
	closing  atomic.Bool // sortie en cours : plus de rafraîchissement
	relaunch bool        // mise à jour réussie : relancer le nouveau binaire après la sortie

	machines  []string
	games     []string
	status    *systray.MenuItem
	agent     *systray.MenuItem
	syncItem  *systray.MenuItem
	auto      *systray.MenuItem
	manager   *systray.MenuItem
	pair      *systray.MenuItem
	forget    *systray.MenuItem
	forgetOne []*systray.MenuItem
	gamesMenu *systray.MenuItem
	gamesMore *systray.MenuItem
	gameSlots []trayGameSlot
	doctor    *systray.MenuItem
	clean     *systray.MenuItem
	update    *systray.MenuItem
}

// launcherRun : action du lanceur en sous-processus, sortie capturée, sans couleurs.
func launcherRun(args ...string) (string, error) {
	cmd := exec.Command(launcherExe(), args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// summarize : dernières lignes utiles d'une sortie, 300 caractères au plus (notification).
func summarize(out string) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return truncate(strings.Join(lines, " · "), 300)
}

// resultSuffix : fin du titre d'une notification selon le code de sortie.
func resultSuffix(err error) string {
	if err == nil {
		return " : terminé"
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf(" : échec (code %d)", ee.ExitCode())
	}
	return " : échec"
}

// onClick : appelle fn à chaque clic sur item.
func onClick(item *systray.MenuItem, fn func()) {
	go func() {
		for range item.ClickedCh {
			fn()
		}
	}()
}

// build : crée le menu (depuis onReady, comme l'exige systray).
func (a *trayApp) build() {
	a.status = systray.AddMenuItem("", "")
	a.status.Disable()

	a.agent = systray.AddMenuItem("", "wsquashfs-launcher --agent")
	onClick(a.agent, a.toggleAgent)
	a.syncItem = systray.AddMenuItem("Synchroniser maintenant", "wsquashfs-launcher --sync")
	onClick(a.syncItem, func() { a.runAction("Synchronisation", "--sync") })
	a.auto = systray.AddMenuItemCheckbox("Synchro auto à la sortie d'un jeu", "wsquashfs-launcher --sync auto", false)
	onClick(a.auto, a.toggleAuto)
	a.manager = systray.AddMenuItem("Ouvrir le manager", "Interface de wsquashfs-manager")
	a.manager.Hide()
	onClick(a.manager, a.openManager)

	systray.AddSeparator()
	a.pair = systray.AddMenuItem("Associer une machine…", "Affiche un code wsq:// à coller dans wsquashfs-manager")
	onClick(a.pair, a.pairMachine)
	a.forget = systray.AddMenuItem("Oublier une machine", "wsquashfs-launcher --pair forget <machine>")
	for i := 0; i < trayMaxMachines; i++ {
		item := a.forget.AddSubMenuItem("", "")
		item.Hide()
		a.forgetOne = append(a.forgetOne, item)
		onClick(item, func() { a.forgetMachine(i) })
	}

	systray.AddSeparator()
	a.gamesMenu = systray.AddMenuItem("Jeux", "Derniers lancés d'abord : lancer un jeu ou copier ses sauvegardes")
	for i := 0; i < trayMaxGames; i++ {
		g := a.gamesMenu.AddSubMenuItem("", "")
		g.Hide()
		launch := g.AddSubMenuItem("Lancer", "")
		save := g.AddSubMenuItem("Copier les sauvegardes", "")
		a.gameSlots = append(a.gameSlots, trayGameSlot{menu: g, launch: launch, save: save})
		onClick(launch, func() { a.launchGame(i) })
		onClick(save, func() { a.saveGame(i) })
	}
	a.gamesMore = a.gamesMenu.AddSubMenuItem("", "")
	a.gamesMore.Disable()
	a.gamesMore.Hide()

	systray.AddSeparator()
	a.doctor = systray.AddMenuItem("Diagnostic", "wsquashfs-launcher --doctor")
	onClick(a.doctor, a.diagnose)
	a.clean = systray.AddMenuItem("Ménage", "wsquashfs-launcher --clean")
	onClick(a.clean, func() { a.runAction("Ménage", "--clean") })
	a.update = systray.AddMenuItem("Mettre à jour", "wsquashfs-launcher --update")
	onClick(a.update, a.updateLauncher)

	systray.AddSeparator()
	quit := systray.AddMenuItem("Quitter", "Ferme l'icône ; l'agent et les jeux restent en marche")
	onClick(quit, a.quit)
}

// setLong : libellé et état d'une entrée d'action longue (désactivée, « … » pendant une action).
// Appelé avec mu tenu.
func (a *trayApp) setLong(item *systray.MenuItem, title string) {
	if a.busy.Load() {
		item.SetTitle(title + " …")
		item.Disable()
		return
	}
	item.SetTitle(title)
	item.Enable()
}

// refresh : recalcule l'état et met à jour libellés et cases.
func (a *trayApp) refresh() {
	if a.closing.Load() {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	_, running := runningAgentPid()
	v := trayState(loadSyncConfig(), running, agentNames(), byLastLaunch(knownGames(), lastLaunch))
	if running {
		a.status.SetTitle("Agent : actif")
		a.setLong(a.agent, "Arrêter l'agent")
	} else {
		a.status.SetTitle("Agent : arrêté")
		a.setLong(a.agent, "Démarrer l'agent")
	}
	a.setLong(a.syncItem, "Synchroniser maintenant")
	if v.AutoSync {
		a.auto.Check()
	} else {
		a.auto.Uncheck()
	}
	if v.ManagerURL != "" {
		a.manager.Show()
	} else {
		a.manager.Hide()
	}
	if a.pairing.Load() {
		a.pair.SetTitle("Associer une machine… (code en attente)")
		a.pair.Disable()
	} else {
		a.pair.SetTitle("Associer une machine…")
		a.pair.Enable()
	}

	a.machines = v.Machines
	for i, item := range a.forgetOne {
		if i < len(v.Machines) {
			item.Show()
			a.setLong(item, v.Machines[i])
		} else {
			item.Hide()
		}
	}
	if len(v.Machines) == 0 {
		a.forget.Disable()
	} else {
		a.forget.Enable()
	}

	a.games = v.Games
	a.gamesMenu.SetTitle(fmt.Sprintf("Jeux (%d)", len(v.Games)+v.GamesMore))
	for i, g := range a.gameSlots {
		if i >= len(v.Games) {
			g.menu.Hide()
			continue
		}
		g.menu.SetTitle(v.Games[i])
		g.menu.Show()
		a.setLong(g.save, "Copier les sauvegardes")
	}
	if v.GamesMore > 0 {
		a.gamesMore.SetTitle(fmt.Sprintf("… %d autres (voir --list)", v.GamesMore))
		a.gamesMore.Show()
	} else {
		a.gamesMore.Hide()
	}

	a.setLong(a.doctor, "Diagnostic")
	a.setLong(a.clean, "Ménage")
	a.setLong(a.update, "Mettre à jour")
}

// begin : réserve l'action longue ; faux (et notification) si une autre tourne déjà.
func (a *trayApp) begin() bool {
	if !a.busy.CompareAndSwap(false, true) {
		notify("wsquashfs-launcher", "Une action est déjà en cours")
		return false
	}
	a.refresh()
	return true
}

// end : libère l'action longue et rafraîchit le menu.
func (a *trayApp) end() {
	a.busy.Store(false)
	a.refresh()
}

// runAction : action longue du lanceur (sous-processus), résultat en notification.
func (a *trayApp) runAction(label string, args ...string) {
	if !a.begin() {
		return
	}
	defer a.end()
	out, err := launcherRun(args...)
	notify(label+resultSuffix(err), summarize(out))
}

// machineAt, gameAt : nom de la ligne i du menu (« Oublier », « Jeux »), "" hors de la liste.
func (a *trayApp) machineAt(i int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i < len(a.machines) {
		return a.machines[i]
	}
	return ""
}

func (a *trayApp) gameAt(i int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i < len(a.games) {
		return a.games[i]
	}
	return ""
}

func (a *trayApp) toggleAgent() {
	if _, running := runningAgentPid(); running {
		a.runAction("Agent : arrêt", "--agent", "stop")
	} else {
		a.runAction("Agent : démarrage", "--agent")
	}
}

// toggleAuto : synchro automatique à la sortie d'un jeu (sync.json).
func (a *trayApp) toggleAuto() {
	cfg := loadSyncConfig()
	next, err := syncAutoSet(cfg, !cfg.Auto)
	if err == nil {
		err = saveSyncConfig(next)
	}
	switch {
	case err != nil:
		notify("Synchro auto non modifiée", err.Error())
	case next.Auto:
		notify("Synchro auto activée", "La partie part au manager à la sortie d'un jeu, si joignable")
	default:
		notify("Synchro auto désactivée", "")
	}
	a.refresh()
}

// openManager : ouvre l'interface du manager déclaré (xdg-open), adresse donnée par sa déclaration.
func (a *trayApp) openManager() {
	p, ok := managerPeer(loadSyncConfig())
	if !ok || p.UI == "" {
		notify("Adresse du manager inconnue", "Le manager la donne en déclarant la machine")
		return
	}
	if err := startDetached("xdg-open", p.UI); err != nil {
		notify("Ouverture du manager impossible", err.Error())
	}
}

// pairMachine : --pair en arrière-plan, sans bloquer les autres actions pendant l'attente ; le code part dans le presse-papiers, le résultat en
// notification (dernière ligne non vide de la sortie).
func (a *trayApp) pairMachine() {
	if !a.pairing.CompareAndSwap(false, true) {
		return
	}
	a.refresh()
	defer func() {
		a.pairing.Store(false)
		a.refresh()
	}()
	r, w, err := os.Pipe()
	if err != nil {
		notify("Association impossible", err.Error())
		return
	}
	cmd := exec.Command(launcherExe(), "--pair")
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		notify("Association impossible", err.Error())
		return
	}
	w.Close()
	var lines []string
	copied := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if code := pairCodeRe.FindString(line); code != "" && !copied {
			copied = true
			if err := copyToClipboard(code); err != nil {
				notify("Code d'association (presse-papiers indisponible)", err.Error()+" : "+code)
			} else {
				notify("Code d'association copié", "Colle-le sur l'autre machine (wsquashfs-launcher --sync wsq://…) ou dans wsquashfs-manager ; usage unique")
			}
		}
	}
	r.Close()
	err = cmd.Wait()
	last := ""
	if len(lines) > 0 {
		last = lines[len(lines)-1]
	}
	notify("Association"+resultSuffix(err), last)
}

// forgetMachine : --pair forget <machine> de la ligne i.
func (a *trayApp) forgetMachine(i int) {
	name := a.machineAt(i)
	if name == "" {
		return
	}
	a.runAction("Oubli de "+name, "--pair", "forget", name)
}

// launchGame : jeu lancé en détaché, par son image mémorisée (ou trouvée dans les roms/).
func (a *trayApp) launchGame(i int) {
	name := a.gameAt(i)
	img := gameImage(name)
	if name == "" || img == "" || !pathExists(img) {
		notify("Lancement impossible", "image de "+name+" introuvable")
		return
	}
	if err := startDetached(launcherExe(), img); err != nil {
		notify("Lancement impossible", err.Error())
		return
	}
	notify("Jeu lancé", name)
}

// saveGame : --save <jeu> de la ligne i.
func (a *trayApp) saveGame(i int) {
	name := a.gameAt(i)
	if name == "" {
		return
	}
	a.runAction("Sauvegardes : "+name, "--save", name)
}

// diagnose : --doctor json ; s'il y a des problèmes, le rapport s'ouvre dans un terminal.
func (a *trayApp) diagnose() {
	if !a.begin() {
		return
	}
	defer a.end()
	out, _ := launcherRun("--doctor", "json") // code 1 si rien ne peut être lancé : le rapport suit
	var rep doctorJSON
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		notify("Diagnostic : réponse illisible", summarize(out))
		return
	}
	problems := 0
	for _, sec := range rep.Sections {
		for _, it := range sec.Items {
			if it.Status == "missing" {
				problems++
			}
		}
	}
	if problems == 0 {
		notify("Diagnostic", rep.Verdict)
		return
	}
	notify("Diagnostic : "+plural(problems, "problème", "problèmes"), "Rapport ouvert dans un terminal")
	term := trayTerminal()
	if term == "" {
		notify("Diagnostic", "Aucun terminal trouvé : lancer wsquashfs-launcher --doctor")
		return
	}
	script := `"$0" "$@"; echo; echo "Entrée pour fermer la fenêtre"; read -r _`
	argv := []string{"sh", "-c", script, launcherExe(), "--doctor"}
	if err := startDetached(term, terminalArgs(term, argv)...); err != nil {
		notify("Terminal impossible", err.Error())
	}
}

// updateLauncher : --update ; en cas de succès, l'icône se relance avec le nouveau binaire.
func (a *trayApp) updateLauncher() {
	if !a.begin() {
		return
	}
	out, err := launcherRun("--update")
	notify("Mise à jour"+resultSuffix(err), summarize(out))
	if err == nil {
		a.relaunch = true
		a.quit()
		return // busy reste tenu : la sortie suit
	}
	a.end()
}

// quit : sortie de l'icône (l'agent et les jeux ne sont pas arrêtés).
func (a *trayApp) quit() {
	a.closing.Store(true)
	systray.Quit()
}

// ready : appelé par systray une fois l'icône enregistrée.
func (a *trayApp) ready() {
	systray.SetIcon(trayIcon)
	systray.SetTooltip("wsquashfs-launcher")
	a.mu.Lock()
	a.build()
	a.mu.Unlock()
	a.refresh()
	go func() {
		t := time.NewTicker(trayRefresh)
		defer t.Stop()
		for range t.C {
			a.refresh()
		}
	}()
}

// exit : retire le fichier de PID en sortant.
func (a *trayApp) exit() {
	_ = os.Remove(trayPidFile())
}

// cmdTray : --tray [on | off].
func cmdTray(args []string) int {
	switch {
	case len(args) == 0:
		return trayRun()
	case len(args) == 1 && (args[0] == "on" || args[0] == "off"):
		return trayAutostartCmd(args[0] == "on")
	}
	uiUsage(trayUsage)
	return 1
}

// trayAutostartCmd : --tray on | off.
func trayAutostartCmd(on bool) int {
	path := trayAutostartPath()
	changed, err := setTrayAutostart(on, launcherExe())
	if err != nil {
		uiStatusLine(stFail, "Lancement à l'ouverture de session : "+err.Error())
		return 1
	}
	switch {
	case on:
		uiStatusLine(stOK, "Icône lancée à l'ouverture de session : "+path)
	case changed:
		uiStatusLine(stOK, "Lancement à l'ouverture de session retiré")
	default:
		uiStatusLine(stSkip, "Pas de lancement à l'ouverture de session")
	}
	return 0
}

// trayRun : l'icône, au premier plan, jusqu'à sa sortie.
func trayRun() int {
	if pid, ok := trayRunning(); ok {
		uiStatusLine(stSkip, fmt.Sprintf("Icône déjà en marche (pid %d)", pid))
		return 0
	}
	if err := trayHostError(); err != nil {
		uiError(err.Error())
		return 1
	}
	if err := os.MkdirAll(agentDir(), 0o700); err != nil {
		uiError("icône impossible : " + err.Error())
		return 1
	}
	if err := os.WriteFile(trayPidFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		uiError("icône impossible : " + err.Error())
		return 1
	}
	app := &trayApp{}
	systray.Run(app.ready, app.exit)
	if app.relaunch {
		// Même PID après exec : le fichier de PID doit être retiré avant.
		_ = os.Remove(trayPidFile())
		exe := launcherExe()
		err := syscall.Exec(exe, []string{exe, "--tray"}, os.Environ())
		uiError("relance impossible : " + err.Error())
		return 1
	}
	return 0
}
