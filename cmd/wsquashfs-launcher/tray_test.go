package main

// Tests de --tray sans D-Bus : état du menu (fonction pure), outils du bureau avec un PATH factice,
// lancement à l'ouverture de session (HOME temporaire), analyse des arguments.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeBin : dossier qui devient le PATH du test, avec des commandes factices (scripts vides).
func fakeBin(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// trayRig : HOME et dossiers de configuration isolés, lanceur factice.
func trayRig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	old := selfPath
	t.Cleanup(func() { selfPath = old })
	selfPath = "/usr/local/bin/wsquashfs-launcher"
	return home
}

// TestTrayState : ce que le menu affiche selon l'agent, le manager, la synchro auto et les listes.
func TestTrayState(t *testing.T) {
	managerCfg := syncConfig{Peers: []syncPeer{
		{Name: "aibox", URL: "https://10.0.0.2:9000"},
		{Name: "wsquashfs-manager", URL: "https://10.0.0.5:8790", Manager: true, UI: "http://10.0.0.5:8765"},
	}}
	cases := []struct {
		name     string
		cfg      syncConfig
		running  bool
		machines []string
		games    []string
		want     trayView
	}{
		{
			name:    "agent arrêté, sans manager, synchro auto inactive",
			cfg:     syncConfig{},
			running: false,
			want:    trayView{},
		},
		{
			name:     "agent actif, manager déclaré, synchro auto active",
			cfg:      syncConfig{Auto: true, Peers: managerCfg.Peers},
			running:  true,
			machines: []string{"wsquashfs-manager"},
			games:    []string{"Hollow Knight"},
			want: trayView{
				AgentRunning: true,
				AutoSync:     true,
				ManagerURL:   "http://10.0.0.5:8765",
				Machines:     []string{"wsquashfs-manager"},
				Games:        []string{"Hollow Knight"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := trayState(c.cfg, c.running, c.machines, c.games)
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Fatalf("trayState = %+v, voulu %+v", got, c.want)
			}
		})
	}
}

// TestTrayStateLimites : au plus 40 jeux et 16 machines ; le reste est compté.
func TestTrayStateLimites(t *testing.T) {
	var games, machines []string
	for i := 0; i < trayMaxGames+5; i++ {
		games = append(games, fmt.Sprintf("jeu %02d", i))
	}
	for i := 0; i < trayMaxMachines+3; i++ {
		machines = append(machines, fmt.Sprintf("machine %02d", i))
	}
	got := trayState(syncConfig{}, false, machines, games)
	if len(got.Games) != trayMaxGames || got.GamesMore != 5 {
		t.Fatalf("jeux : %d affichés, %d en plus ; voulu %d et 5", len(got.Games), got.GamesMore, trayMaxGames)
	}
	if len(got.Machines) != trayMaxMachines {
		t.Fatalf("machines : %d affichées ; voulu %d", len(got.Machines), trayMaxMachines)
	}
}

// TestClipboardTools : outils retenus selon la session (Wayland, X11) et le PATH ; sans outil,
// copyToClipboard dit quoi installer.
func TestClipboardTools(t *testing.T) {
	names := func() string {
		var n []string
		for _, c := range clipboardTools() {
			n = append(n, c.name)
		}
		return strings.Join(n, ",")
	}
	fakeBin(t, "wl-copy", "xclip", "xsel")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")
	if got := names(); got != "wl-copy" {
		t.Fatalf("Wayland seul : %q, voulu wl-copy", got)
	}
	t.Setenv("DISPLAY", ":0")
	if got := names(); got != "wl-copy,xclip,xsel" {
		t.Fatalf("Wayland et XWayland : %q", got)
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	if got := names(); got != "xclip,xsel" {
		t.Fatalf("X11 : %q", got)
	}
	fakeBin(t, "xsel")
	if got := names(); got != "xsel" {
		t.Fatalf("X11 avec xsel seul : %q", got)
	}
	fakeBin(t, "wl-copy")
	if got := names(); got != "" {
		t.Fatalf("X11 avec wl-copy seul : %q, voulu aucun", got)
	}
	if err := copyToClipboard("wsq://x"); err != errNoClipboard {
		t.Fatalf("sans outil : %v, voulu errNoClipboard", err)
	}
}

// TestTrayTerminal : $TERMINAL d'abord, puis le premier terminal connu ; "" si aucun.
func TestTrayTerminal(t *testing.T) {
	fakeBin(t, "xterm", "konsole")
	t.Setenv("TERMINAL", "")
	if got := trayTerminal(); filepath.Base(got) != "konsole" {
		t.Fatalf("trayTerminal = %q ; voulu konsole", got)
	}
	fakeBin(t, "xterm", "ma-console")
	t.Setenv("TERMINAL", "ma-console")
	if got := trayTerminal(); filepath.Base(got) != "ma-console" {
		t.Fatalf("trayTerminal = %q ; voulu ma-console (TERMINAL)", got)
	}
	fakeBin(t)
	t.Setenv("TERMINAL", "")
	if got := trayTerminal(); got != "" {
		t.Fatalf("trayTerminal = %q sans terminal ; voulu \"\"", got)
	}
}

// TestTerminalArgs : option d'exécution propre à chaque terminal.
func TestTerminalArgs(t *testing.T) {
	argv := []string{"sh", "-c", "echo bonjour", "wsquashfs-launcher", "--doctor"}
	if got := strings.Join(terminalArgs("/usr/bin/gnome-terminal", argv), " "); !strings.HasPrefix(got, "-- sh -c") {
		t.Fatalf("gnome-terminal : %q", got)
	}
	if got := strings.Join(terminalArgs("xterm", argv), " "); !strings.HasPrefix(got, "-e sh -c") {
		t.Fatalf("xterm : %q", got)
	}
	got := terminalArgs("xfce4-terminal", argv)
	if len(got) != 2 || got[0] != "-e" || !strings.Contains(got[1], "'echo bonjour'") {
		t.Fatalf("xfce4-terminal : %q", got)
	}
}

// TestDesktopExec : chemin avec blancs entre guillemets, caractères réservés échappés.
func TestDesktopExec(t *testing.T) {
	if got := desktopExec("/usr/local/bin/wsquashfs-launcher"); got != "/usr/local/bin/wsquashfs-launcher" {
		t.Fatalf("chemin simple : %q", got)
	}
	if got := desktopExec("/opt/jeux livres/wsq"); got != `"/opt/jeux livres/wsq"` {
		t.Fatalf("chemin avec blanc : %q", got)
	}
}

// TestTrayAutostart : --tray on crée le fichier avec la bonne ligne Exec, off le retire ; un
// argument inconnu est refusé sans rien écrire.
func TestTrayAutostart(t *testing.T) {
	home := trayRig(t)
	path := filepath.Join(home, ".config", "autostart", "wsquashfs-launcher-tray.desktop")

	if code := cmdTray([]string{"bidule"}); code != 1 {
		t.Fatalf("--tray bidule : code %d ; voulu 1", code)
	}
	if isFile(path) {
		t.Fatal("fichier créé pour un argument refusé")
	}

	if code := cmdTray([]string{"on"}); code != 0 {
		t.Fatalf("--tray on : code %d", code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"Exec=/usr/local/bin/wsquashfs-launcher --tray run\n",
		"Type=Application\n",
		"Name=wsquashfs-launcher\n",
		"X-GNOME-Autostart-enabled=true\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("fichier d'autostart sans %q :\n%s", want, text)
		}
	}

	if code := cmdTray([]string{"off"}); code != 0 {
		t.Fatalf("--tray off : code %d", code)
	}
	if isFile(path) {
		t.Fatal("fichier encore présent après --tray off")
	}
	if code := cmdTray([]string{"off"}); code != 0 {
		t.Fatalf("--tray off sans fichier : code %d ; voulu 0", code)
	}
}

// TestTrayAutostartSansLanceur : pas de chemin du lanceur, pas de fichier.
func TestTrayAutostartSansLanceur(t *testing.T) {
	trayRig(t)
	selfPath = ""
	if code := cmdTray([]string{"on"}); code != 1 {
		t.Fatalf("--tray on sans chemin : code %d ; voulu 1", code)
	}
}

// TestTrayPidObsolete : un fichier de PID qui ne désigne pas une icône ne compte pas.
func TestTrayPidObsolete(t *testing.T) {
	trayRig(t)
	if err := os.MkdirAll(agentDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trayPidFile(), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := trayRunning(); ok {
		t.Fatal("trayRunning vrai pour un processus qui n'est pas une icône")
	}
}

// TestParseArgsTray : --tray et ses arguments passent à la commande.
func TestParseArgsTray(t *testing.T) {
	o, err := parseArgs([]string{"--tray", "on"})
	if err != nil {
		t.Fatal(err)
	}
	if o.action != "tray" || len(o.cmdArgs) != 1 || o.cmdArgs[0] != "on" {
		t.Fatalf("parseArgs(--tray on) = %q %v", o.action, o.cmdArgs)
	}
}

func TestByLastLaunch(t *testing.T) {
	base := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	when := map[string]time.Time{"B": base, "C": base.Add(time.Hour)}
	got := byLastLaunch([]string{"A", "B", "C", "D"}, func(g string) time.Time { return when[g] })
	want := []string{"C", "B", "A", "D"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("byLastLaunch = %v, attendu %v", got, want)
	}
}

// TestManagerUIURL : adresse de l'interface donnée par le manager à sa déclaration.
func TestManagerUIURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"": true, "http://10.0.0.5:8765": true, "https://manager.lan/wsq/": true,
		"ftp://x": false, "javascript:alert(1)": false, "http://": false, "http://u:p@h": false,
	} {
		if _, err := managerUIURL(raw); (err == nil) != ok {
			t.Errorf("managerUIURL(%q) : erreur %v, valide attendu %v", raw, err, ok)
		}
	}
}

// TestPairCodeRe : le code est repéré dans la sortie de --pair, pas la note qui la suit ou la précède.
func TestPairCodeRe(t *testing.T) {
	code := pairCode("s3cr3t_-X", "192.168.1.20", 8790, strings.Repeat("AB", 32))
	out := "Colle ce code sur l'autre machine (wsquashfs-launcher --sync wsq://…)\n  " + code + "\n"
	if got := pairCodeRe.FindString(out); got != code {
		t.Fatalf("code repéré %q, voulu %q", got, code)
	}
	if got := pairCodeRe.FindString("wsquashfs-launcher --sync wsq://…)"); got != "" {
		t.Fatalf("note prise pour un code : %q", got)
	}
}

// TestTrayStop : --tray stop sans icône (rien à faire, fichier de PID périmé retiré) ; arguments
// refusés.
func TestTrayStop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := os.MkdirAll(agentDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trayPidFile(), []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := cmdTray([]string{"stop"}); code != 0 {
		t.Fatalf("--tray stop sans icône : code %d", code)
	}
	if isFile(trayPidFile()) {
		t.Fatal("fichier de PID périmé gardé")
	}
	if code := cmdTray([]string{"stop", "x"}); code != 1 {
		t.Fatalf("--tray stop x : code %d ; voulu 1", code)
	}
}
