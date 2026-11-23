package main

// OptiScaler (23/11) : OPTISCALER=1 (ou le nom de DLL sous lequel il se
// charge : dxgi, winmm, version, dbghelp, d3d12, wininet, winhttp) relie
// OptiScaler, installé par --setup optiscaler, dans le dossier de
// l'exécutable. Clé d'interrupteur : couche machine, dossier, surcharge ou
// --set, jamais l'autorun.cmd figé dans l'image.
//
// Liens posés à chaque lancement et retirés quand la clé disparaît (ils vont
// dans la couche de sauvegardes) ; un fichier du jeu n'est jamais remplacé :
// le remplacer puis le retirer laisserait un masque (whiteout) sur celui de
// l'image. OptiScaler.ini est copié une fois : les réglages faits dans le
// menu (Inser) restent propres au jeu.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"wsquashfs-launcher/internal/filetree"
	"wsquashfs-launcher/internal/ui"
)

// optiscalerDllNames : noms sous lesquels OptiScaler peut être chargé.
var optiscalerDllNames = []string{"dxgi", "winmm", "version", "dbghelp", "d3d12", "wininet", "winhttp"}

// optiscalerBase : OptiScaler installé par --setup (fichier VERSION : sa version).
func optiscalerBase() string {
	return filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/optiscaler")
}

// findOptiscalerDir : dossier d'OptiScaler s'il est installé, "" sinon.
func findOptiscalerDir() string {
	if d := optiscalerBase(); isFile(d + "/OptiScaler.dll") {
		return d
	}
	return ""
}

// optiscalerVersion : version installée (fichier VERSION), "" si inconnue.
func optiscalerVersion() string {
	data, err := os.ReadFile(optiscalerBase() + "/VERSION")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// optiscalerDllName : valeur de OPTISCALER → nom de DLL ("" : désactivé).
// ok faux pour une valeur inconnue.
func optiscalerDllName(v string) (name string, ok bool) {
	v = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), ".dll")
	switch {
	case v == "" || v == "0":
		return "", true
	case v == "1":
		return "dxgi", true
	case slices.Contains(optiscalerDllNames, v):
		return v, true
	}
	return "", false
}

// optiscalerGameDir : dossier de l'exécutable dans le prefix monté, "" hors
// du prefix (programme de la machine).
func optiscalerGameDir() string {
	if launch.execPath == "" || !strings.HasPrefix(launch.execPath, launch.winePoint+"/") {
		return ""
	}
	return filepath.Dir(launch.execPath)
}

// isOptiscalerLink : lien posé par le lanceur, reconnu à la fin du chemin de
// sa cible : HOME peut changer d'un lancement à l'autre (SteamBox :
// /home/arcade, lien vers /config).
func isOptiscalerLink(p string) bool {
	t, err := os.Readlink(p)
	return err == nil && strings.Contains(t, "/.local/share/wsquashfs/optiscaler/")
}

// resolveOptiscaler : OptiScaler retenu (launch.optiscalerDll) si la clé le
// demande, qu'il est installé et que le jeu n'a pas déjà cette DLL.
func resolveOptiscaler() {
	launch.optiscalerDll = ""
	name, ok := optiscalerDllName(launch.optiscaler)
	switch {
	case !ok:
		ui.Warn("OPTISCALER=" + launch.optiscaler + " ignoré : 0, 1 ou un nom de DLL (" + strings.Join(optiscalerDllNames, ", ") + ")")
		return
	case name == "":
		return
	case findOptiscalerDir() == "":
		ui.Warn("OPTISCALER ignoré : OptiScaler non installé (--setup optiscaler)")
		return
	case optiscalerGameDir() == "":
		ui.Warn("OPTISCALER ignoré : exécutable hors du prefix")
		return
	}
	own := optiscalerGameDir() + "/" + name + ".dll"
	if _, err := os.Lstat(own); err == nil && !isOptiscalerLink(own) {
		ui.Warn("OPTISCALER ignoré : le jeu a déjà son " + name + ".dll (OPTISCALER=winmm, version… pour un autre nom)")
		return
	}
	launch.optiscalerDll = name
}

// optiscalerSkipped : fichiers de l'archive jamais reliés (doc, scripts
// d'installation Windows/Linux) ; fakenvapi seulement sans carte NVIDIA
// (la doc d'OptiScaler le réserve à AMD et Intel).
func optiscalerSkipped(name string) bool {
	lower := strings.ToLower(name)
	switch {
	case lower == "optiscaler.dll", lower == "optiscaler.ini", lower == "licenses", lower == "version":
		return true
	case strings.HasPrefix(lower, "setup_"), strings.HasSuffix(lower, ".txt"), strings.HasSuffix(lower, ".md"):
		return true
	case strings.HasPrefix(lower, "fakenvapi"):
		return launch.hasNvidia == "1"
	}
	return false
}

// installOptiscaler : retire les liens d'OptiScaler du dossier de l'exécutable,
// puis les repose s'il est retenu. Rend les fichiers du jeu gardés à sa place.
func installOptiscaler() (kept []string) {
	dir := optiscalerGameDir()
	if dir == "" {
		return nil
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if p := dir + "/" + e.Name(); isOptiscalerLink(p) {
			_ = os.Remove(p)
		}
	}
	src := findOptiscalerDir()
	if launch.optiscalerDll == "" || src == "" {
		return nil
	}
	link := func(from, name string) {
		dst := dir + "/" + name
		if _, err := os.Lstat(dst); err == nil {
			kept = append(kept, name)
			return
		}
		_ = os.Symlink(from, dst)
	}
	link(src+"/OptiScaler.dll", launch.optiscalerDll+".dll")
	files, _ := os.ReadDir(src)
	for _, f := range files {
		if !optiscalerSkipped(f.Name()) {
			link(src+"/"+f.Name(), f.Name())
		}
	}
	if ini := dir + "/OptiScaler.ini"; !pathExists(ini) {
		if filetree.CopyFile(src+"/OptiScaler.ini", ini, 0o644) == nil {
			chownToTarget(ini)
		}
	}
	return kept
}

// setupOptiscaler : liens posés, ligne du récapitulatif.
func setupOptiscaler() {
	kept := installOptiscaler()
	if launch.optiscalerDll == "" {
		return
	}
	msg := strings.TrimSpace("OptiScaler "+optiscalerVersion()) + " : " + launch.optiscalerDll + ".dll (menu : Inser)"
	if len(kept) > 0 {
		msg += " ; fichiers du jeu gardés : " + strings.Join(kept, ", ")
	}
	ui.StatusLine(ui.Info, msg)
}
