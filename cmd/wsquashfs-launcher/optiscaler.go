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
// menu (Inser) restent propres au jeu. À cette copie seulement, les modèles
// partiels (couche machine puis dossier) remplacent ou ajoutent leurs clés.

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
			launch.optiscalerModeles = appliquerModelesOptiscaler(ini)
		}
	}
	return kept
}

// appliquerModelesOptiscaler : fusionne les modèles partiels dans l'ini fraîchement
// copié, machine puis dossier (le dossier l'emporte). Retourne les modèles appliqués.
func appliquerModelesOptiscaler(ini string) (appliques []string) {
	modeles := []struct{ label, path string }{
		{"modèle machine", filepath.Join(configHome(), "wsquashfs/optiscaler.ini")},
		{"modèle dossier", filepath.Join(filepath.Dir(strings.TrimSuffix(launch.wsquashfsFile, "/")), "_optiscaler.ini")},
	}
	data, err := os.ReadFile(ini)
	if err != nil {
		return nil
	}
	base := string(data)
	for _, m := range modeles {
		if !isFile(m.path) {
			continue
		}
		over, err := os.ReadFile(m.path)
		if err != nil {
			continue
		}
		base = mergeIniOverrides(base, string(over))
		appliques = append(appliques, m.label)
	}
	if len(appliques) == 0 {
		return nil
	}
	if os.WriteFile(ini, []byte(base), 0o644) != nil {
		return nil
	}
	chownToTarget(ini)
	return appliques
}

// iniSection : une section d'un modèle partiel, clés dans l'ordre du fichier.
type iniSection struct {
	nom  string
	cles []string
	vals map[string]string
}

// parseIniModele : sections et clés d'un modèle partiel. Les lignes vides,
// commentaires (; ou #) et lignes sans « = » ou hors section sont ignorés ; une
// clé répétée garde sa dernière valeur.
func parseIniModele(texte string) []iniSection {
	var sections []iniSection
	idx := map[string]int{}
	cur := -1
	for _, ligne := range strings.Split(texte, "\n") {
		t := strings.TrimSpace(ligne)
		switch {
		case t == "" || t[0] == ';' || t[0] == '#':
		case iniEnTete(t) != "":
			nom := iniEnTete(t)
			if _, ok := idx[nom]; !ok {
				idx[nom] = len(sections)
				sections = append(sections, iniSection{nom: nom, vals: map[string]string{}})
			}
			cur = idx[nom]
		case cur >= 0 && strings.Contains(t, "="):
			k, v, _ := strings.Cut(t, "=")
			k = strings.TrimSpace(k)
			if _, ok := sections[cur].vals[k]; !ok {
				sections[cur].cles = append(sections[cur].cles, k)
			}
			sections[cur].vals[k] = strings.TrimSpace(v)
		}
	}
	return sections
}

// iniEnTete : nom de la section si la ligne (déjà sans espaces) est « [Nom] », "" sinon.
func iniEnTete(t string) string {
	if len(t) >= 2 && t[0] == '[' && t[len(t)-1] == ']' {
		return strings.TrimSpace(t[1 : len(t)-1])
	}
	return ""
}

// mergeIniOverrides : applique le modèle partiel override à base. Pour chaque clé
// d'une section du modèle, la ligne « Clé=… » de cette section est remplacée
// (commentaires et autres lignes gardés) ; une clé absente de la section est
// ajoutée à la fin de la section, une section absente en fin de fichier. Les
// fins de ligne de base (CRLF ou LF) sont conservées.
func mergeIniOverrides(base, override string) string {
	modele := parseIniModele(override)
	if len(modele) == 0 {
		return base
	}
	retour := ""
	if strings.Contains(base, "\r\n") {
		retour = "\r"
	}
	var lignes []string
	if base != "" {
		lignes = strings.Split(base, "\n")
	}
	trouvees := map[string]bool{}   // sections de base déjà vues
	appliquees := map[string]bool{} // clés du modèle posées (« section\x00clé »)
	sectionDe := func(nom string) *iniSection {
		for i := range modele {
			if modele[i].nom == nom {
				return &modele[i]
			}
		}
		return nil
	}
	var out []string
	var cur *iniSection // section du modèle active, nil hors modèle
	fin := 0            // indice après la dernière ligne utile de la section courante
	// clore insère les clés du modèle absentes de la section qui se termine.
	clore := func() {
		if cur == nil {
			return
		}
		var nouvelles []string
		for _, k := range cur.cles {
			if !appliquees[cur.nom+"\x00"+k] {
				nouvelles = append(nouvelles, k+"="+cur.vals[k]+retour)
			}
		}
		out = slices.Insert(out, fin, nouvelles...)
		cur = nil
	}
	for _, brut := range lignes {
		corps := strings.TrimSuffix(brut, "\r")
		retour := ""
		if corps != brut {
			retour = "\r"
		}
		t := strings.TrimSpace(corps)
		if nom := iniEnTete(t); nom != "" {
			clore()
			trouvees[nom] = true
			out = append(out, brut)
			fin = len(out)
			cur = sectionDe(nom)
			continue
		}
		k, _, cle := strings.Cut(t, "=")
		cle = cle && cur != nil && t != "" && t[0] != ';' && t[0] != '#'
		k = strings.TrimSpace(k)
		if cle {
			if v, dans := cur.vals[k]; dans && !appliquees[cur.nom+"\x00"+k] {
				appliquees[cur.nom+"\x00"+k] = true
				out = append(out, k+"="+v+retour)
				fin = len(out)
				continue
			}
		}
		out = append(out, brut)
		if cle {
			fin = len(out)
		}
	}
	clore()
	ajoute := false
	for i := range modele {
		s := &modele[i]
		if trouvees[s.nom] {
			continue
		}
		out = append(out, "["+s.nom+"]"+retour)
		for _, k := range s.cles {
			out = append(out, k+"="+s.vals[k]+retour)
		}
		ajoute = true
	}
	if ajoute {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
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
	if len(launch.optiscalerModeles) > 0 {
		msg += " ; OptiScaler.ini : " + strings.Join(launch.optiscalerModeles, ", ") + " appliqué"
	}
	ui.StatusLine(ui.Info, msg)
}
