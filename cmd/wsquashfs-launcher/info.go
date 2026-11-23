package main

// Commandes --info et --show : lecture des informations d'une image (autorun, runners, clés).

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"wsquashfs-launcher/internal/savesync"
	"wsquashfs-launcher/internal/ui"
)

// ── --info (29/09) et --show (06/11) ───────────────────────────────────

// imageAutorun : autorun.cmd d'une image (sans la monter) ou d'un dossier.
func imageAutorun(p string) string {
	if isDirPath(p) {
		data, _ := os.ReadFile(strings.TrimSuffix(p, "/") + "/autorun.cmd")
		return string(data)
	}
	return squashfsCat(p, "autorun.cmd")
}

// infoRunner : ligne Runner de --info.
func infoRunner(autorun, joined string) string {
	var key, name string
	for _, k := range []string{"RUNNER", "PROTON", "WINE"} {
		key = k
		name = keyLine(autorun, k)
		if name != "" {
			break
		}
	}
	if key == "RUNNER" && name != "" {
		if strings.HasPrefix(name, "/") {
			return name + " (machine)"
		}
		return "RUNNER=" + name + " refusé (chemin absolu attendu ; runner joint : runners/<nom>/ et PROTON=)"
	}
	if name != "" {
		for _, j := range strings.Split(joined, "\n") {
			if strings.EqualFold(j, name) || strings.EqualFold(j, "wine-"+name) {
				return name + " (joint à l'image)"
			}
		}
		return name + " (runner de la machine)"
	}
	var names []string
	for _, j := range strings.Split(joined, "\n") {
		if j != "" {
			names = append(names, j)
		}
	}
	switch len(names) {
	case 1:
		return names[0] + " (joint à l'image)"
	case 0:
		return "défaut (choisi au lancement)"
	}
	return "défaut — plusieurs runners joints (" + strings.Join(names, ", ") + ") : PROTON= ou WINE= à préciser"
}

// keyLine : valeur d'une clé CLÉ= (première occurrence, \r retiré), "" sinon.
func keyLine(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimRight(strings.TrimPrefix(line, key+"="), "\r")
		}
	}
	return ""
}

// showInfo : --info <jeu | dossier>.
func showInfo(args []string) int {
	if len(args) == 0 {
		return usage(os.Args[0] + " --info <jeu.wsquashfs | dossier du jeu>")
	}
	src, err := filepath.Abs(args[0])
	if err != nil || !pathExists(src) {
		return usage(os.Args[0] + " --info <jeu.wsquashfs | dossier du jeu>")
	}
	if real, err := filepath.EvalSymlinks(src); err == nil {
		src = real
	}
	var autorun, proton, joined string
	typ := ""
	if isDirPath(src) {
		autorun = imageAutorunDir(src)
		if isFile(src + "/system.reg") {
			typ = "prefix"
		}
		proton = firstLineOf(src + "/config_info")
		joined = listRunnerDirs(src + "/runners")
	} else {
		autorun = squashfsCat(src, "autorun.cmd")
		if execQuiet("unsquashfs", "-cat", src, "system.reg") == nil {
			typ = "prefix"
		}
		proton = squashfsFirstLine(src, "config_info")
		joined = squashfsRunnerNames(src)
	}
	proton = strings.TrimRight(proton, "\r")
	version := gameVersion(src)
	switch {
	case proton != "":
		typ = "prefix Proton (" + proton + ")"
	case typ == "prefix":
		typ = "prefix Wine (Batocera)"
	default:
		typ = "jeu seul (prefix créé au premier lancement)"
	}
	ui.Title(savesync.GameNameOf(src))
	if version == "" {
		version = "non renseignée (GAME_VERSION= dans l'autorun.cmd)"
	}
	pairs := []ui.Pair{{"Version", version}, {"Type", typ}}
	if cmdLine, ok := overrideVar(strings.TrimSuffix(src, "/")+".autorun", "CMD"); ok {
		pairs = append(pairs, ui.Pair{"Commande", cmdLine})
	} else if autorun != "" {
		pairs = append(pairs, ui.Pair{"Commande", keyLine(autorun, "CMD")})
	} else {
		pairs = append(pairs, ui.Pair{"Commande", "aucun autorun.cmd"})
	}
	pairs = append(pairs, ui.Pair{"Runner", infoRunner(autorun, joined)})
	if isFile(strings.TrimSuffix(src, "/") + ".autorun") {
		override := strings.TrimSuffix(src, "/") + ".autorun"
		pairs = append(pairs, ui.Pair{"Surcharge", filepath.Base(override) + " (" + overrideKeys(override) + ") — non lue par Batocera"})
	}
	pairs = append(pairs, ui.Pair{"Taille", duHuman(src)})
	ui.KV(0, pairs...)
	return 0
}

// imageAutorunDir : autorun.cmd d'un dossier installé.
func imageAutorunDir(d string) string {
	data, _ := os.ReadFile(d + "/autorun.cmd")
	return string(data)
}

// firstLineOf : première ligne d'un fichier, "" si absent.
func firstLineOf(p string) string {
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.SplitN(string(data), "\n", 2)[0]
}

// squashfsFirstLine : première ligne d'un fichier de l'image.
func squashfsFirstLine(image, file string) string {
	return strings.SplitN(squashfsCat(image, file), "\n", 2)[0]
}

// listRunnerDirs : noms des dossiers de runners joints, un par ligne.
func listRunnerDirs(dir string) string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return strings.Join(names, "\n")
}

// squashfsRunnerNames : dossiers runners/<nom> d'une image (unsquashfs -l).
func squashfsRunnerNames(image string) string {
	out, err := exec.Command("unsquashfs", "-l", image, "runners").Output()
	if err != nil {
		return ""
	}
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(line, "/")
		if len(parts) >= 4 && !seen[parts[2]] {
			seen[parts[2]] = true
			names = append(names, parts[2])
		}
	}
	slices.Sort(names)
	return strings.Join(names, "\n")
}

// cmdShow : --show <jeu> [--set CLÉ=valeur]... (réglages finaux et couches).
func cmdShow(args []string) int {
	src := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--set" {
			if v, err := parseSet(argAt(args, i+1)); err == nil {
				launch.setArgs = append(launch.setArgs, v)
			} else {
				return fail(err.Error())
			}
			i++
			continue
		}
		if src != "" {
			return usage(os.Args[0] + " --show <jeu.wsquashfs | dossier> [--set CLÉ=valeur]...")
		}
		src = args[i]
	}
	if src == "" || !pathExists(src) {
		return usage(os.Args[0] + " --show <jeu.wsquashfs | dossier> [--set CLÉ=valeur]...")
	}
	if real, err := filepath.Abs(src); err == nil {
		src = real
	}
	launch.wsquashfsFile = src
	launch.gameName = savesync.GameNameOf(src)
	launch.autorunOverride = strings.TrimSuffix(src, "/") + ".autorun"
	if !isFile(launch.autorunOverride) {
		launch.autorunOverride = ""
	}
	loadLayers(imageAutorun(src))

	ui.Title(launch.gameName)
	machine := launch.machineLayer
	if machine == "" {
		machine = machineLayerFile() + " (absent)"
	}
	folder := launch.folderLayer
	if folder == "" {
		folder = filepath.Dir(src) + "/_defaults.autorun (absent)"
	}
	surcharge := strings.TrimSuffix(src, "/") + ".autorun"
	if launch.autorunOverride == "" {
		surcharge += " (absent)"
	}
	couches := []ui.Pair{{"machine", machine}, {"dossier", folder}, {"image", "autorun.cmd"}, {"surcharge", surcharge}}
	if len(launch.setArgs) > 0 {
		couches = append(couches, ui.Pair{"--set", strings.Join(launch.setArgs, " ")})
	}
	ui.Section("Couches (de la plus faible à la plus forte)")
	ui.KV(2, couches...)

	shown := map[string]bool{}
	for _, k := range showKeys {
		shown[k] = true
	}
	var extra []string
	for k := range layers.machineVal {
		if !shown[k] {
			extra = append(extra, k)
		}
	}
	for k := range layers.layerVal {
		if !shown[k] {
			extra = append(extra, k)
		}
	}
	slices.Sort(extra)
	extra = uniqueSorted(extra)
	var reglages [][]string
	for _, key := range append(append([]string{}, showKeys...), extra...) {
		layerLookup(key, "")
		if layers.lookupVal == "" && layers.lookupSrc == "défaut" {
			continue
		}
		reglages = append(reglages, []string{key, layers.lookupVal, layers.lookupSrc})
	}
	ui.Section("Réglages")
	ui.Table([]string{"Réglage", "Valeur", "Couche"}, reglages, nil, nil)

	if layers.envLayers > 0 {
		ui.Section("ENV")
		if mergedEnv() == "" {
			ui.Note("aucune variable : ENV= vide")
		}
		var env [][]string
		seen := map[string]bool{}
		for _, n := range layers.envOrder {
			if _, ok := layers.envVal[n]; !ok || seen[n] {
				continue
			}
			seen[n] = true
			env = append(env, []string{layers.envVal[n], layers.envSrc[n]})
		}
		if len(env) > 0 {
			ui.Table([]string{"Variable", "Couche"}, env, nil, nil)
		}
	}

	layerLookup("SAVEDIR", "")
	key := layers.lookupVal
	layerLookup("SAVEFILES", "")
	sysdir, ok := batoceraSavesDir()
	overlay := ui.Pair{"Overlay", conf.savesDir + "/" + launch.gameName}
	sauvegardes := ui.Pair{"Sauvegardes", "dans l'overlay ; --save <jeu> les copie dans " + sysdir}
	switch {
	case !ok:
		sauvegardes[1] = "hors d'un dossier roms/<système>/ (WSQUASHFS_BATOCERA_SAVES)"
	case key+layers.lookupVal != "":
		sauvegardes[1] = sysdir + " (SAVEDIR/SAVEFILES)"
	}
	ui.Section("Sauvegardes")
	ui.KV(2, overlay, sauvegardes)
	return 0
}

// showKeys : clés affichées par --show, dans cet ordre.
var showKeys = []string{"CMD", "DIR", "GAME_VERSION", "PROTON", "WINE", "RUNNER", "ARCH", "DXVK", "VKD3D", "D7VK",
	"NVAPI", "VKREFLEX", "FSR", "FSR4", "OPTISCALER", "ESYNC", "FSYNC", "HIDRAW", "LANG", "VIRTUAL_DESKTOP", "SAVEDIR",
	"SAVEFILES", "PRE", "ATTACH", "ATTACH_PROCESS"}
