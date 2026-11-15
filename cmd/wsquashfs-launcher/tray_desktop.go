package main

// Côté bureau de --tray : notifications (D-Bus), presse-papiers, terminal, ouverture d'une
// adresse, lancement à l'ouverture de session (fichier .desktop d'autostart). Rien ici ne
// dépend de l'état du lanceur : tray.go le relie au menu.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"
)

// trayAutostartPath : ~/.config/autostart/wsquashfs-launcher-tray.desktop.
func trayAutostartPath() string {
	return filepath.Join(os.Getenv("HOME"), ".config", "autostart", "wsquashfs-launcher-tray.desktop")
}

// trayDesktopEntry : fichier d'autostart qui lance l'icône avec ce lanceur.
func trayDesktopEntry(exe string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=wsquashfs-launcher\n" +
		"Comment=Icône de zone de notification\n" +
		"Exec=" + desktopExec(exe) + " --tray\n" +
		"X-GNOME-Autostart-enabled=true\n"
}

// desktopExec : chemin du lanceur pour la clé Exec (entre guillemets s'il contient des blancs ou
// des caractères spéciaux, avec les caractères réservés échappés).
func desktopExec(exe string) string {
	if !strings.ContainsAny(exe, " \t\"`$\\") {
		return exe
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return `"` + r.Replace(exe) + `"`
}

// setTrayAutostart : écrit (on) ou supprime (off) le fichier d'autostart. Déjà dans l'état demandé :
// vrai sans rien faire de plus ; le résultat dit si le fichier a changé.
func setTrayAutostart(on bool, exe string) (bool, error) {
	path := trayAutostartPath()
	if !on {
		if !isFile(path) {
			return false, nil
		}
		return true, os.Remove(path)
	}
	if exe == "" {
		return false, errors.New("chemin du lanceur inconnu")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(trayDesktopEntry(exe)), 0o644)
}

// trayHostError : bus de session et hôte de zone de notification (StatusNotifierWatcher) présents ;
// message lisible sinon.
func trayHostError() error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return fmt.Errorf("pas de bus de session D-Bus (%v) : lancer l'icône depuis une session graphique", err)
	}
	var has bool
	call := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher")
	if call.Err != nil {
		return fmt.Errorf("bus de session illisible (%v)", call.Err)
	}
	if err := call.Store(&has); err != nil {
		return fmt.Errorf("bus de session illisible (%v)", err)
	}
	if !has {
		return errors.New("aucune zone de notification : sous GNOME, installer l'extension AppIndicator")
	}
	return nil
}

// notify : notification de bureau (org.freedesktop.Notifications). Échec ignoré : l'icône ne dépend
// pas d'un démon de notifications.
func notify(summary, body string) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return
	}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	_ = obj.Call("org.freedesktop.Notifications.Notify", 0, "wsquashfs-launcher", uint32(0), "",
		summary, body, []string{}, map[string]dbus.Variant{}, int32(-1)).Err
}

// clipboardTool : premier outil de presse-papiers trouvé (wl-copy, xclip, xsel) et ses arguments.
func clipboardTool() (string, []string, bool) {
	tools := []struct {
		name string
		args []string
	}{
		{"wl-copy", nil},
		{"xclip", []string{"-selection", "clipboard"}},
		{"xsel", []string{"--clipboard", "--input"}},
	}
	for _, t := range tools {
		if _, err := exec.LookPath(t.name); err == nil {
			return t.name, t.args, true
		}
	}
	return "", nil, false
}

// copyToClipboard : met text dans le presse-papiers (erreur si aucun outil n'est installé).
func copyToClipboard(text string) error {
	name, args, ok := clipboardTool()
	if !ok {
		return errors.New("aucun outil de presse-papiers (wl-copy, xclip ou xsel)")
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// trayTerminal : premier terminal disponible, $TERMINAL d'abord ; "" si aucun.
func trayTerminal() string {
	names := []string{"x-terminal-emulator", "konsole", "gnome-terminal", "xfce4-terminal", "xterm"}
	if t := os.Getenv("TERMINAL"); t != "" {
		names = append([]string{t}, names...)
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// terminalArgs : arguments de terminal pour lancer argv dans une fenêtre (option d'exécution propre
// à chaque terminal : « -- » pour gnome-terminal, une seule chaîne pour xfce4-terminal).
func terminalArgs(term string, argv []string) []string {
	switch filepath.Base(term) {
	case "gnome-terminal":
		return append([]string{"--"}, argv...)
	case "xfce4-terminal":
		quoted := make([]string, len(argv))
		for i, a := range argv {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		return []string{"-e", strings.Join(quoted, " ")}
	}
	return append([]string{"-e"}, argv...)
}

// startDetached : lance un programme sans attendre (nouvelle session, entrées et sorties nulles) ;
// le processus est récolté en arrière-plan.
func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
