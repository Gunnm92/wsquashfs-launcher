package main

// Runners Wine et Proton, runner joint à l'image, choix du runner (script
// bash, sections 5 et 9.6).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// sysPath : chemin sous SYSROOT (vide en usage normal ; les tests le font
// pointer sur un dossier vide).
func sysPath(p string) string {
	return sysroot + p
}

// installedWines : binaires bin/wine installés par l'installeur ou à la main,
// filtrés par filter (sous-chaîne, insensible à la casse ; "" : tous).
func installedWines(filter *regexp.Regexp) []string {
	var out []string
	home := os.Getenv("HOME")
	for _, d := range []string{filepath.Join(home, ".local/share/wsquashfs/wine"), "/opt"} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		for _, w := range findAll([]string{d}, 3, func(p string) bool {
			return filepath.Base(p) == "wine" && strings.HasSuffix(p, "/bin/wine")
		}) {
			if filter != nil && !filter.MatchString(w) {
				continue
			}
			if isExecutable(w) {
				out = append(out, w)
			}
		}
	}
	return out
}

// findAll : tous les chemins acceptés par match, parcourus jusqu'à maxDepth.
func findAll(roots []string, maxDepth int, match func(string) bool) []string {
	var out []string
	for _, root := range roots {
		for {
			hit := findIn([]string{root}, maxDepth, func(p string) bool {
				if !match(p) {
					return false
				}
				for _, seen := range out {
					if seen == p {
						return false
					}
				}
				return true
			})
			if hit == "" {
				break
			}
			out = append(out, hit)
		}
	}
	return out
}

var (
	reTkg = regexp.MustCompile(`(?i)tkg`)
	reGE  = regexp.MustCompile(`(?i)/(wine-ge|lutris-|wine-lutris)`)
)

// findWineBinary : Wine par défaut (prefix non Proton). Priorité : wine-tkg,
// wine-ge/lutris, wine-proton, puis le Wine système.
func findWineBinary() string {
	var candidates []string
	candidates = append(candidates, sysPath("/usr/wine/wine-tkg/bin/wine"))
	candidates = append(candidates, installedWines(reTkg)...)
	candidates = append(candidates, sysPath("/usr/wine/ge-custom/bin/wine"))
	candidates = append(candidates, installedWines(reGE)...)
	candidates = append(candidates, sysPath("/usr/wine/wine-proton/bin/wine"))
	for _, c := range candidates {
		if !isExecutable(c) {
			continue
		}
		if elfInterpreterPresent(c) {
			return c
		}
		// Repli : wine64 dans le même dossier bin/
		w := strings.TrimSuffix(c, "wine") + "wine64"
		if isExecutable(w) && elfInterpreterPresent(w) {
			return w
		}
	}
	if p, err := exec.LookPath("wine"); err == nil {
		return p
	}
	return ""
}

// wineSupportsWin32 : le binaire sait exécuter un prefix win32 (i386-unix).
func wineSupportsWin32(bin string) bool {
	root := runnerRoot(bin)
	for _, d := range []string{root + "/lib/wine/i386-unix", root + "/lib32/wine/i386-unix"} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// findWin32Wine : premier Wine installé capable de lancer un prefix win32.
func findWin32Wine() string {
	roots := []string{sysPath("/usr/wine"), sysPath("/opt"), filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/wine")}
	for _, w := range findAll(roots, 3, func(p string) bool {
		return filepath.Base(p) == "wine" && strings.HasSuffix(p, "/bin/wine")
	}) {
		if wineSupportsWin32(w) {
			return w
		}
	}
	return ""
}

// protonRunnerDirs : runners Proton installés hors de nos dossiers (Heroic,
// Steam, paquets). Un dossier vu deux fois n'est listé qu'une fois.
func protonRunnerDirs() []string {
	home := os.Getenv("HOME")
	seen := map[string]bool{}
	var out []string
	for _, base := range []string{
		filepath.Join(home, ".config/heroic/tools/proton"),
		filepath.Join(home, ".local/share/Steam/compatibilitytools.d"),
		filepath.Join(home, ".steam/root/compatibilitytools.d"),
		sysPath("/usr/share/steam/compatibilitytools.d"),
	} {
		entries, _ := filepath.Glob(base + "/*")
		for _, d := range entries {
			if !isExecutable(d + "/files/bin/wine") {
				continue
			}
			real, err := filepath.EvalSymlinks(d)
			if err != nil {
				real = d
			}
			if !seen[real] {
				seen[real] = true
				out = append(out, real)
			}
		}
	}
	return out
}

// listWineRunners : runners installés, pour --wine.
func listWineRunners() []string {
	var out []string
	home := os.Getenv("HOME")
	var dirs []string
	for _, pat := range []string{sysPath("/opt") + "/*", sysPath("/usr/wine") + "/*", filepath.Join(home, ".local/share/wsquashfs/wine") + "/*"} {
		m, _ := filepath.Glob(pat)
		dirs = append(dirs, m...)
	}
	for _, d := range dirs {
		if isExecutable(d+"/bin/wine") || isExecutable(d+"/files/bin/wine") {
			out = append(out, d)
		}
	}
	return append(out, protonRunnerDirs()...)
}

// findUmuRun : umu-run (paquet umu-launcher, ou celui de Heroic).
// WSQUASHFS_UMU=0 : Wine lancé directement.
func findUmuRun() string {
	if os.Getenv("WSQUASHFS_UMU") == "0" {
		return ""
	}
	if p, err := exec.LookPath("umu-run"); err == nil && isExecutable(p) {
		return p
	}
	heroic := filepath.Join(os.Getenv("HOME"), ".config/heroic/tools/runtimes/umu/umu_run.py")
	if isExecutable(heroic) {
		return heroic
	}
	return ""
}

// prefixProtonVersion : version de Proton qui a créé le prefix (1re ligne de
// config_info), vide pour un prefix Batocera ou Wine simple.
func prefixProtonVersion() string {
	data, err := os.ReadFile(winePoint + "/config_info")
	if err != nil {
		return ""
	}
	first := strings.SplitN(string(data), "\n", 2)[0]
	return strings.TrimRight(first, "\r")
}

// protonRunnerIs : le dossier correspond à cette version (nom ou fichier version).
func protonRunnerIs(d, want string) bool {
	if filepath.Base(d) == want {
		return true
	}
	data, err := os.ReadFile(d + "/version")
	if err != nil {
		return false
	}
	fields := strings.Fields(strings.SplitN(string(data), "\n", 2)[0])
	return len(fields) > 0 && fields[len(fields)-1] == want
}

// versionLess : comparaison de type sort -V (nombres comparés en valeur).
func versionLess(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		if unicode.IsDigit(ra[i]) && unicode.IsDigit(rb[j]) {
			si, sj := i, j
			for i < len(ra) && unicode.IsDigit(ra[i]) {
				i++
			}
			for j < len(rb) && unicode.IsDigit(rb[j]) {
				j++
			}
			na, _ := strconv.ParseInt(string(ra[si:i]), 10, 64)
			nb, _ := strconv.ParseInt(string(rb[sj:j]), 10, 64)
			if na != nb {
				return na < nb
			}
			continue
		}
		if ra[i] != rb[j] {
			return ra[i] < rb[j]
		}
		i++
		j++
	}
	return len(ra)-i < len(rb)-j
}

// lastVersion : le dernier (sort -V | tail -1) des chemins qui matchent.
func lastVersion(paths []string, re *regexp.Regexp) string {
	var hits []string
	for _, p := range paths {
		if re.MatchString(p) {
			hits = append(hits, p)
		}
	}
	if len(hits) == 0 {
		return ""
	}
	sort.Slice(hits, func(i, j int) bool { return versionLess(hits[i], hits[j]) })
	return hits[len(hits)-1]
}

var (
	reCachyos  = regexp.MustCompile(`(?i)proton-cachyos`)
	reUmu      = regexp.MustCompile(`(?i)/UMU-Proton`)
	reGEProton = regexp.MustCompile(`(?i)/GE-Proton`)
)

// findProtonRunner : dossier du runner Proton pour une version voulue, sinon le
// plus récent des proton-cachyos, puis UMU-Proton, GE-Proton, puis n'importe
// quel Proton (un prefix Proton ne doit pas retomber sur le Wine système).
func findProtonRunner(want string) string {
	dirs := protonRunnerDirs()
	if len(dirs) == 0 {
		return ""
	}
	if want != "" {
		for _, d := range dirs {
			if protonRunnerIs(d, want) {
				return d
			}
		}
	}
	if d := lastVersion(dirs, reCachyos); d != "" {
		return d
	}
	if d := lastVersion(dirs, reUmu); d != "" {
		return d
	}
	if d := lastVersion(dirs, reGEProton); d != "" {
		return d
	}
	return dirs[0]
}

// resolveWineOverride : --wine <version|chemin>. Retour vide si introuvable.
func resolveWineOverride(want string) string {
	if want == "system" {
		if p, err := exec.LookPath("wine"); err == nil {
			return p
		}
		return ""
	}
	if isFile(want) && isExecutable(want) {
		return want
	}
	candidates := []string{want}
	for _, d := range listWineRunners() {
		if strings.Contains(d, want) {
			candidates = append(candidates, d)
		}
	}
	for _, d := range candidates {
		if isExecutable(d + "/bin/wine") {
			return d + "/bin/wine"
		}
		if isExecutable(d + "/files/bin/wine") {
			return d + "/files/bin/wine"
		}
	}
	return ""
}

// protonDirOf : dossier Proton d'un binaire …/files/bin/wine (avec proton).
func protonDirOf(bin string) string {
	if !strings.HasSuffix(bin, "/files/bin/wine") {
		return ""
	}
	root := strings.TrimSuffix(bin, "/files/bin/wine")
	if !isExecutable(root + "/proton") {
		return ""
	}
	return root
}

// ── Runner joint à l'image ─────────────────────────────────────────────

// imageRoot : racine de l'image en lecture seule (l'image montée, ou le prefix
// en mode copy ou dossier).
func imageRoot() string {
	if mountMode == "overlay" && squashfsPoint != "" {
		return squashfsPoint
	}
	return winePoint
}

// runnerWineOf : binaire wine d'un dossier de runner (Proton ou Wine).
func runnerWineOf(d string) string {
	if isExecutable(d + "/files/bin/wine") {
		return d + "/files/bin/wine"
	}
	if isExecutable(d + "/bin/wine") {
		return d + "/bin/wine"
	}
	return ""
}

// imageRunnerDirs : dossiers runners/<nom> de l'image.
func imageRunnerDirs() []string {
	m, _ := filepath.Glob(imageRoot() + "/runners/*")
	var out []string
	for _, d := range m {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

// findImageRunner : binaire wine du runner <want> joint à l'image.
func findImageRunner(want string) string {
	lower := strings.ToLower(want)
	for _, d := range imageRunnerDirs() {
		name := strings.ToLower(filepath.Base(d))
		if name == lower || name == "wine-"+lower || protonRunnerIs(d, want) {
			if w := runnerWineOf(d); w != "" {
				return w
			}
		}
	}
	return ""
}

// singleImageRunner : nom du runner joint s'il est seul dans runners/.
func singleImageRunner() string {
	var found []string
	for _, d := range imageRunnerDirs() {
		if runnerWineOf(d) != "" {
			found = append(found, filepath.Base(d))
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

// resolveImageRunner : PROTON= / WINE= (ou runner seul) → imageRunner.
func resolveImageRunner() {
	imageRunner = ""
	if runnerKey != "" {
		if !strings.HasPrefix(runnerKey, "/") && wineOverride == "" {
			die(fmt.Sprintf("RUNNER=%s : chemin absolu de la machine attendu", runnerKey),
				"  → runner joint au jeu : dossier runners/<nom>/ de l'image et PROTON=<nom> (ou WINE=<nom>)")
		}
		return
	}
	if protonVersion == "" && wineVersion == "" {
		name := singleImageRunner()
		if name == "" {
			if st, err := os.Stat(imageRoot() + "/runners"); err == nil && st.IsDir() {
				warn("runners/ de l'image sans runner unique, aucun désigné (PROTON=/WINE=) : runner par défaut")
			}
			return
		}
		if path := findImageRunner(name); protonDirOf(path) != "" {
			protonVersion = name
		} else {
			wineVersion = name
		}
	}
	want := protonVersion
	if want == "" {
		want = wineVersion
	}
	imageRunner = findImageRunner(want)
}

// ── 9.6 Choix du runner ────────────────────────────────────────────────

// chooseProtonDefault : prefix créé par Proton, ou jeu seul sans prefix : Proton
// via umu. Le choix de l'autorun.cmd (PROTON=) l'emporte.
func chooseProtonDefault() {
	prefixProton = prefixProtonVersion()
	if wineOverride != "" {
		if found := resolveWineOverride(wineOverride); found != "" && protonDirOf(found) != "" &&
			(prefixProton != "" || !isFile(winePoint+"/system.reg")) {
			overrideProton = protonDirOf(found)
		}
	}
	forcedWine = runnerKey + wineVersion
	if wineOverride != "" && overrideProton == "" {
		forcedWine += wineOverride
	}
	// Image sans prefix : prefix créé par Proton (proton-cachyos, UMU, GE) ;
	// un prefix Wine déjà créé dans les sauvegardes reste sous Wine.
	if prefixProton == "" && !packagedPrefix && forcedWine == "" {
		if !isFile(winePoint + "/system.reg") {
			autoProton = true
		} else if dirMode {
			warn("prefix Wine déjà créé dans " + winePoint + ", gardé sous " + runnerName(wineBinary) + " (Proton ne sait pas le convertir)")
		} else {
			warn("prefix Wine déjà créé dans les sauvegardes, gardé sous " + runnerName(wineBinary) +
				" — supprimer " + savePoint + " (sauvegardes du jeu comprises) pour passer sous Proton")
		}
	}
	if protonVersion != "" {
		return
	}
	if prefixProton == "" && !autoProton {
		return
	}
	if found := findProtonRunner(prefixProton); found != "" {
		wineBinary = found + "/files/bin/wine"
		if prefixProton != "" && !protonRunnerIs(found, prefixProton) {
			warn("prefix créé par " + prefixProton + " (absent), utilisation de " + filepath.Base(found))
		}
	} else {
		if prefixProton != "" {
			warn("prefix Proton (" + prefixProton + ") voulu, aucun Proton installé, utilisation de " + runnerName(wineBinary))
		} else {
			warn("prefix Proton voulu, aucun Proton installé, utilisation de " + runnerName(wineBinary))
		}
		autoProton = false
	}
}

// applyAutorunRunner : runner imposé par l'autorun.cmd (RUNNER, PROTON, WINE),
// joint à l'image d'abord, puis sur la machine. Introuvable : erreur.
func applyAutorunRunner() {
	if wineOverride != "" {
		return
	}
	switch {
	case runnerKey != "":
		if !isExecutable(runnerKey) {
			die("runner "+runnerKey+" introuvable ou non exécutable",
				"  → --wine <runner> pour en utiliser un autre")
		}
		wineBinary = runnerKey
	case protonVersion != "":
		if imageRunner != "" {
			wineBinary = imageRunner
		} else if found := findProtonRunner(protonVersion); found != "" && protonRunnerIs(found, protonVersion) {
			wineBinary = found + "/files/bin/wine"
		} else if len(protonRunnerDirs()) == 0 && isExecutable(sysPath("/usr/local/bin/proton")) {
			wineBinary = sysPath("/usr/local/bin/proton")
		} else {
			die("Proton "+protonVersion+" introuvable",
				"  cherché : runners/ de l'image, Heroic, Steam (compatibilitytools.d)",
				"  → le joindre à l'image (runners/"+protonVersion+"/), l'installer, ou --wine <runner>")
		}
	case wineVersion != "":
		found := imageRunner
		if found == "" && strings.EqualFold(wineVersion, "system") {
			if p, err := exec.LookPath("wine"); err == nil {
				found = p
			}
		}
		for _, d := range []string{sysPath("/opt/wine-" + wineVersion), filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/wine/wine-"+wineVersion)} {
			if found != "" {
				break
			}
			if isExecutable(d + "/bin/wine") {
				found = d + "/bin/wine"
			}
		}
		if found == "" {
			die("Wine "+wineVersion+" introuvable",
				"  cherché : runners/ de l'image, /opt/wine-"+wineVersion+", ~/.local/share/wsquashfs/wine/wine-"+wineVersion,
				"  → le joindre à l'image (runners/"+wineVersion+"/), l'installer, ou --wine <runner>")
		}
		wineBinary = found
	}
}

// applyWineOption : --wine (ligne de commande), prioritaire sur l'autorun.cmd.
func applyWineOption() {
	if wineOverride == "" {
		return
	}
	if found := resolveWineOverride(wineOverride); found != "" {
		wineBinary = found
		return
	}
	fmt.Fprintln(os.Stderr, "Erreur : runner Wine introuvable : "+wineOverride)
	fmt.Fprintln(os.Stderr, "  Runners installés :")
	for _, r := range listWineRunners() {
		fmt.Fprintln(os.Stderr, "    "+r)
	}
	wineAbs, err := exec.LookPath("wine")
	if err != nil {
		wineAbs = "absent"
	}
	fmt.Fprintln(os.Stderr, "    system ("+wineAbs+")")
	exitNow(1)
}

// setupUmu : prefix Proton ou jeu seul : lancé par umu-run (script proton
// complet), comme Heroic.
func setupUmu() {
	if (prefixProton+protonVersion+overrideProton != "" || autoProton) && forcedWine == "" {
		if prefixProton == "" && isFile(winePoint+"/system.reg") {
			wineBinary = findWineBinary()
			warn("PROTON=" + protonVersion + " ignoré, prefix Wine existant — Proton ne sait pas le convertir")
		} else if dir := protonDirOf(wineBinary); dir != "" && findUmuRunLookup() != "" {
			umuRun = findUmuRunLookup()
			protonDir = dir
			if os.Getenv("GAMEID") == "" {
				os.Setenv("GAMEID", "umu-default")
			}
			os.Setenv("PROTONPATH", protonDir)
			if wineBinary == imageRunner {
				os.Setenv("PYTHONDONTWRITEBYTECODE", "1")
			}
		} else if autoProton {
			umuRun = ""
			wineBinary = findWineBinary()
			warn("umu-run absent, prefix créé par " + runnerName(wineBinary) + " au lieu de Proton")
		} else {
			umuRun = ""
			warn("prefix Proton lancé directement par Wine (umu-run ou Proton absent) — rendu ou stabilité possiblement dégradés")
		}
	}
}

// findUmuRunLookup : findUmuRun, en tenant compte de WSQUASHFS_UMU.
func findUmuRunLookup() string {
	return findUmuRun()
}

// selectRunner : Wine par défaut, runner joint, Proton du prefix, autorun.cmd,
// --wine, puis umu.
func selectRunner() {
	wineBinary = findWineBinary()
	resolveImageRunner()
	chooseProtonDefault()
	applyAutorunRunner()
	applyWineOption()
	setupUmu()
}
