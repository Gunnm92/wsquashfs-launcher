package main

// Lecture de l'autorun.cmd et réglages par couches (script bash, section 3).
//
// Du plus faible au plus fort : défauts du lanceur, machine (fichier, puis
// WSQUASHFS_*), dossier (_defaults.autorun), image (autorun.cmd), surcharge
// (<jeu>.wsquashfs.autorun), --set. ENV= se fusionne variable par variable.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Défauts des clés (KEY_DEFAULT) et variables d'environnement machine
// (KEY_ENVVAR) du script.
var keyDefault = map[string]string{
	"DXVK": "1", "VKD3D": "1", "D7VK": "1", "ESYNC": "0", "FSYNC": "0",
	"NVAPI": "auto", "VKREFLEX": "auto", "HIDRAW": "0", "FSR": "0", "FSR4": "0",
}

var keyEnvVar = map[string]string{
	"DXVK": "WSQUASHFS_DXVK", "D7VK": "WSQUASHFS_D7VK", "NVAPI": "WSQUASHFS_NVAPI",
	"VKREFLEX": "WSQUASHFS_VKREFLEX", "VIRTUAL_DESKTOP": "WSQUASHFS_VIRTUAL_DESKTOP",
	"HIDRAW": "WSQUASHFS_HIDRAW", "FSR": "WSQUASHFS_FSR", "FSR4": "WSQUASHFS_FSR4",
}

// État des couches lues.
var (
	machineVal = map[string]string{}
	layerVal   = map[string]string{}
	layerSrc   = map[string]string{}
	envVal     = map[string]string{}
	envSrc     = map[string]string{}
	envOrder   []string
	envLayers  int
	envRaw     string
)

// Valeur finale et couche qui l'a fournie (LOOKUP_VAL, LOOKUP_SRC).
var lookupVal, lookupSrc string

var keyLineRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// machineLayerFile : ~/.config/wsquashfs/defaults.autorun.
func machineLayerFile() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(base, "wsquashfs/defaults.autorun")
}

// overrideVar : valeur d'une clé d'un fichier de surcharge (ok faux si absente).
func overrideVar(file, key string) (string, bool) {
	if file == "" || !isFile(file) {
		return "", false
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", false
	}
	prefix := key + "="
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimRight(strings.TrimPrefix(line, prefix), "\r"), true
		}
	}
	return "", false
}

// overrideKeys : clés présentes dans un fichier, séparées par des espaces.
func overrideKeys(file string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	var keys []string
	for _, line := range strings.Split(string(data), "\n") {
		if m := keyLineRe.FindStringSubmatch(line); m != nil {
			keys = append(keys, m[1])
		}
	}
	return strings.Join(keys, " ")
}

// mergeEnvLayer : fusionne une valeur ENV= dans envVal. -NOM retire une
// variable d'une couche plus faible, ENV= vide les retire toutes.
func mergeEnvLayer(src, raw string) {
	envLayers++
	envRaw = raw
	if raw == "" {
		envVal = map[string]string{}
		envSrc = map[string]string{}
		envOrder = nil
		return
	}
	for _, tok := range strings.Fields(raw) {
		if strings.HasPrefix(tok, "-") && !strings.Contains(tok, "=") {
			name := tok[1:]
			if name == "" {
				continue
			}
			delete(envVal, name)
			delete(envSrc, name)
			continue
		}
		name := tok
		if i := strings.Index(tok, "="); i >= 0 {
			name = tok[:i]
		}
		if name == "" {
			continue
		}
		if _, seen := envVal[name]; !seen {
			envOrder = append(envOrder, name)
		}
		envVal[name] = tok
		envSrc[name] = src
	}
}

// mergedEnv : ENV= final. Une seule couche avec un ENV= : la ligne telle
// qu'écrite.
func mergedEnv() string {
	if envLayers <= 1 {
		return envRaw
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range envOrder {
		v, ok := envVal[n]
		if !ok || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, v)
	}
	return strings.Join(out, " ")
}

// readLayer : première occurrence de chaque clé d'une couche ; fins de ligne
// DOS tolérées.
func readLayer(src, content string) {
	seen := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.ReplaceAll(line, "\r", "")
		m := keyLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key, val := m[1], m[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		switch {
		case key == "ENV":
			mergeEnvLayer(src, val)
		case src == "machine":
			machineVal[key] = val
		default:
			layerVal[key] = val
			layerSrc[key] = src
		}
	}
}

// loadLayers : lit toutes les couches. image = contenu de l'autorun.cmd de
// l'image ; wsquashfsFile et autorunOverride doivent être posés.
func loadLayers(image string) {
	machineVal = map[string]string{}
	layerVal = map[string]string{}
	layerSrc = map[string]string{}
	envVal = map[string]string{}
	envSrc = map[string]string{}
	envOrder = nil
	envLayers = 0
	envRaw = ""
	machineLayer, folderLayer = "", ""

	if f := machineLayerFile(); isFile(f) {
		machineLayer = f
		data, _ := os.ReadFile(f)
		readLayer("machine", string(data))
	}
	if wsquashfsFile != "" {
		f := filepath.Join(filepath.Dir(strings.TrimSuffix(wsquashfsFile, "/")), "_defaults.autorun")
		if isFile(f) {
			folderLayer = f
			data, _ := os.ReadFile(f)
			readLayer("dossier", string(data))
		}
	}
	readLayer("image", image)
	if autorunOverride != "" && isFile(autorunOverride) {
		data, _ := os.ReadFile(autorunOverride)
		readLayer("surcharge", string(data))
	}
	if len(setArgs) > 0 {
		readLayer("--set", strings.Join(setArgs, "\n"))
	}
}

// layerLookup : valeur finale d'une clé (lookupVal) et sa couche (lookupSrc).
func layerLookup(key, def string) {
	envvar := keyEnvVar[key]
	if v, ok := keyDefault[key]; ok {
		lookupVal = v
	} else {
		lookupVal = def
	}
	lookupSrc = "défaut"
	if v := machineVal[key]; v != "" {
		lookupVal = v
		lookupSrc = "machine"
	}
	if envvar != "" {
		if v := os.Getenv(envvar); v != "" {
			lookupVal = v
			lookupSrc = "machine (" + envvar + ")"
		}
	}
	if src, ok := layerSrc[key]; ok {
		if v := layerVal[key]; v != "" {
			lookupVal = v
			lookupSrc = src
		} else {
			lookupSrc += " (" + src + " : vide)"
		}
	}
	// WSQUASHFS_HIDRAW passe avant tout (choose_hidraw_mode)
	if key == "HIDRAW" {
		if v := os.Getenv("WSQUASHFS_HIDRAW"); v != "" {
			lookupVal = v
			lookupSrc = "machine (WSQUASHFS_HIDRAW, prioritaire)"
		}
	}
}

// autorunVar : valeur finale d'une clé, toutes couches confondues.
func autorunVar(key, def string) string {
	if key == "ENV" {
		return mergedEnv()
	}
	layerLookup(key, def)
	return lookupVal
}

var gameVersionRe = regexp.MustCompile(`(?m)^GAME_VERSION=(.*)$`)

// gameVersion : GAME_VERSION= de l'autorun.cmd (surcharge .autorun d'abord),
// lu dans l'image sans la monter, ou dans un dossier installé.
func gameVersion(path string) string {
	if v, ok := overrideVar(strings.TrimSuffix(path, "/")+".autorun", "GAME_VERSION"); ok {
		return unquote(v)
	}
	var content string
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		data, _ := os.ReadFile(strings.TrimSuffix(path, "/") + "/autorun.cmd")
		content = string(data)
	} else {
		content = squashfsCat(path, "autorun.cmd")
	}
	m := gameVersionRe.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return unquote(strings.TrimRight(m[1], "\r"))
}
