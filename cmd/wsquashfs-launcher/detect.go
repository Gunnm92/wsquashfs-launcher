package main

// Dossier de jeu sans autorun.cmd (jeu brut, sans suffixe .pc) : lancé comme
// un dossier .pc, en overlay. CMD et DIR sont cherchés parmi les .exe du
// dossier. Un choix sûr ne laisse aucune trace ; en cas de doute, un mémo
// <dossier>.autorun est écrit pour être relu et corrigé.

import (
	"cmp"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

// detectMaxDepth : nombre de niveaux de dossiers fouillés sous le dossier du jeu.
const detectMaxDepth = 4

// Exclusions reprises de wsquashfs-manager (analyze.py : _EXCLUDED_DIRS,
// _EXCLUDED_DIR_PARTS, _EXCLUDED_NAMES et le moteur Unreal). Le score complet
// du manager n'est pas repris.
var detectExcludedPrefixes = []string{
	"drive_c/windows/", "drive_c/programdata/", "drive_c/users/",
	"drive_c/program files/common files/", "drive_c/program files (x86)/common files/",
}

var detectExcludedDirs = map[string]bool{
	"_commonredist": true, "__installer": true, "redist": true, "redistributables": true,
	"directx": true, "vcredist": true, "dotnet": true, "easyanticheat": true, "battleye": true,
	"prereqs": true, "prerequisites": true, "installers": true, "_redist": true, "_crack": true,
	"_cracks": true, "crack": true, "cracks": true, "_original files": true, "original files": true,
	"_original": true, "__macosx": true, "tools": true,
	// Ajouts du lanceur (essai sur de vrais dossiers Steam/Epic, 18/11) :
	// outils de rétroéclairage ASUS livrés avec certains jeux.
	"aurasdk": true,
}

var detectExcludedNames = []struct{ pattern, reason string }{
	{"vcredist*", "redistribuable"}, {"vc_redist*", "redistribuable"},
	{"dxsetup*", "installeur DirectX"}, {"dxwebsetup*", "installeur DirectX"},
	{"ue4prereqsetup*", "prérequis Unreal"}, {"ueprereqsetup*", "prérequis Unreal"},
	{"dotnetfx*", "installeur .NET"}, {"ndp*-kb*", "installeur .NET"},
	{"unins*", "désinstalleur"}, {"*uninstall*", "désinstalleur"},
	{"*setup*", "installeur"}, {"*install*", "installeur"},
	{"unitycrashhandler*", "rapporteur de plantage"},
	{"crashreportclient*", "rapporteur de plantage"},
	{"*crashhandler*", "rapporteur de plantage"}, {"*crashpad*", "rapporteur de plantage"},
	{"*crashreport*", "rapporteur de plantage"}, {"*crack*", "intro de crack"},
	{"quicksfv*", "outil de vérification"}, {"dxdiag*", "outil système"},
	{"easyanticheat*", "anti-triche"}, {"eac*launcher*", "anti-triche"},
	// Ajouts du lanceur (18/11) : CrashSender (DiRT 4, WRC), serveurs dédiés.
	{"crashsender*", "rapporteur de plantage"}, {"*dedicatedserver*", "serveur dédié"},
}

// detectExcluded : raison d'exclure un fichier (chemin relatif au dossier du
// jeu), vide s'il reste candidat.
func detectExcluded(rel string) string {
	low := strings.ToLower(filepath.ToSlash(rel))
	for _, p := range detectExcludedPrefixes {
		if strings.HasPrefix(low, p) {
			return "dossier système du prefix"
		}
	}
	parts := strings.Split(low, "/")
	for _, p := range parts[:len(parts)-1] {
		if strings.HasSuffix(p, ".app") {
			return "paquet macOS"
		}
		if detectExcludedDirs[p] {
			return "dossier d'installeurs ou de redistribuables"
		}
	}
	if strings.Contains(low, "engine/binaries/") || strings.Contains(low, "engine/extras/") {
		return "outil du moteur Unreal"
	}
	name := parts[len(parts)-1]
	for _, e := range detectExcludedNames {
		if ok, _ := path.Match(e.pattern, name); ok {
			return e.reason
		}
	}
	return ""
}

// exeCandidat : un .exe du dossier du jeu.
type exeCandidat struct {
	rel    string // chemin relatif au dossier du jeu, séparateurs /
	taille int64
}

// detectExes : .exe du dossier (casse ignorée, liens non suivis, profondeur
// limitée), triés par chemin ; exclus à part, avec leur raison.
func detectExes(root string) (cands []exeCandidat, exclus []string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if strings.Count(rel, string(filepath.Separator)) >= detectMaxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(d.Name()), ".exe") {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if why := detectExcluded(relSlash); why != "" {
			exclus = append(exclus, relSlash+" ("+why+")")
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		cands = append(cands, exeCandidat{rel: relSlash, taille: info.Size()})
		return nil
	})
	slices.SortFunc(cands, func(a, b exeCandidat) int { return cmp.Compare(a.rel, b.rel) })
	slices.Sort(exclus)
	return cands, exclus
}

// detectNorm : nom en minuscules, lettres et chiffres seulement.
func detectNorm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// detectDistance : distance de Levenshtein entre deux chaînes.
func detectDistance(a, b []rune) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// detectSimilarite : proximité de deux noms, de 0 à 1 (casse et ponctuation
// ignorées).
func detectSimilarite(a, b string) float64 {
	na, nb := []rune(detectNorm(a)), []rune(detectNorm(b))
	if len(na) == 0 || len(nb) == 0 {
		return 0
	}
	return 1 - float64(detectDistance(na, nb))/float64(max(len(na), len(nb)))
}

// detectChoisir : candidat le plus probable, nom le plus proche de celui du
// dossier ; à égalité, le plus gros fichier.
func detectChoisir(cands []exeCandidat, nomDossier string) exeCandidat {
	best, bestSim := cands[0], -1.0
	for _, c := range cands {
		s := detectSimilarite(nomDossier, strings.TrimSuffix(path.Base(c.rel), filepath.Ext(c.rel)))
		if s > bestSim || (s == bestSim && c.taille > best.taille) {
			best, bestSim = c, s
		}
	}
	return best
}

// detectRacineSure : le seul .exe à la racine est un choix sûr si son nom
// ressemble à celui du dossier, ou s'il est le lanceur racine d'un jeu Unreal
// (tous les autres dans <Projet>/Binaries/Win64). Sinon (Cyberpunk 2077 :
// REDprelauncher.exe à la racine, le jeu dans bin/x64), mémo.
func detectRacineSure(racine exeCandidat, cands []exeCandidat, nomDossier string) bool {
	if detectSimilarite(nomDossier, strings.TrimSuffix(racine.rel, filepath.Ext(racine.rel))) >= 0.5 {
		return true
	}
	for _, c := range cands {
		if c.rel == racine.rel {
			continue
		}
		low := strings.ToLower(c.rel)
		if !strings.Contains(low, "/binaries/win64/") && !strings.Contains(low, "/binaries/win32/") {
			return false
		}
	}
	return true
}

// detectValeur : valeur d'une clé, entre guillemets si elle contient un espace.
func detectValeur(v string) string {
	if strings.ContainsAny(v, " \t") {
		return `"` + v + `"`
	}
	return v
}

// detectLignes : DIR= et CMD= d'un exécutable relatif au dossier du jeu, fins
// de ligne LF (les lignes sont lues par readLayer, sans retour chariot).
func detectLignes(rel string) string {
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	return "DIR=" + detectValeur(dir) + "\nCMD=" + detectValeur(path.Base(rel)) + "\n"
}

// ecrireMemo : écrit le mémo <dossier>.autorun (fins de ligne CRLF) : une ligne
// REM d'en-tête, les autres candidats en REM, puis DIR= et CMD= du choix.
func ecrireMemo(memo, nomDossier string, choisi exeCandidat, autres []exeCandidat) error {
	var b strings.Builder
	b.WriteString("REM Mémo du lanceur, " + time.Now().Format("2006-01-02") + " : plusieurs exécutables possibles dans " + nomDossier + ", modifiable\r\n")
	b.WriteString("REM Autres candidats :\r\n")
	for _, c := range autres {
		b.WriteString("REM   " + c.rel + "\r\n")
	}
	b.WriteString(strings.ReplaceAll(detectLignes(choisi.rel), "\n", "\r\n"))
	if err := os.WriteFile(memo, []byte(b.String()), 0o644); err != nil {
		return err
	}
	chownToTarget(memo)
	return nil
}

// ensureFolderCmd : dossier sans autorun.cmd lancé en overlay et sans CMD dans
// les couches : cherche l'exécutable parmi les .exe du dossier.
func ensureFolderCmd() {
	if !launch.folderOverlay || launch.action == "exec" || isFile(launch.wsquashfsFile+"/autorun.cmd") {
		return
	}
	loadLayers("")
	if autorunVar("CMD", "") != "" {
		return
	}
	dossier := strings.TrimSuffix(launch.wsquashfsFile, "/")
	nom := filepath.Base(dossier)
	cands, exclus := detectExes(dossier)
	if len(cands) == 0 {
		details := []string{}
		for _, e := range exclus {
			details = append(details, "  exclu : "+e)
		}
		die("aucun exécutable de jeu dans "+dossier,
			append(details,
				"  → --set CMD=Jeu.exe (chemin relatif au dossier), ou --exec programme",
				"  → ou un fichier "+dossier+".autorun avec DIR= et CMD=")...)
	}

	// Un seul candidat, ou un seul à la racine : choix sûr, rien n'est écrit.
	var racine []exeCandidat
	for _, c := range cands {
		if !strings.Contains(c.rel, "/") {
			racine = append(racine, c)
		}
	}
	if len(cands) == 1 || (len(racine) == 1 && detectRacineSure(racine[0], cands, nom)) {
		choisi := cands[0]
		if len(cands) > 1 {
			choisi = racine[0]
		}
		launch.detectLayer = detectLignes(choisi.rel)
		uiKVLine("Exécutable", choisi.rel+" (trouvé dans le dossier)")
		return
	}

	// Plusieurs candidats : le plus probable, et un mémo à relire.
	choisi := detectChoisir(cands, nom)
	var autres []exeCandidat
	for _, c := range cands {
		if c.rel != choisi.rel {
			autres = append(autres, c)
		}
	}
	memo := dossier + ".autorun"
	conseils := []string{
		"  → vérifier ou modifier " + memo + " (DIR= et CMD=)",
		"  → ou renommer le dossier en " + nom + ".pc et l'analyser dans le manager",
	}
	switch {
	case pathExists(memo):
		// Mémo déjà là sans CMD : on ne l'écrase pas.
		launch.detectLayer = detectLignes(choisi.rel)
		warn("plusieurs exécutables possibles dans "+dossier+" ; "+memo+" existe sans CMD, "+choisi.rel+" lancé", conseils...)
	default:
		if err := ecrireMemo(memo, nom, choisi, autres); err != nil {
			launch.detectLayer = detectLignes(choisi.rel)
			warn("plusieurs exécutables possibles dans "+dossier+" ; mémo impossible à écrire ("+err.Error()+"), "+choisi.rel+" lancé", conseils[1:]...)
			return
		}
		launch.autorunOverride = memo
		warn("plusieurs exécutables possibles dans "+dossier+" ; "+choisi.rel+" retenu, mémo "+memo+" écrit", conseils...)
	}
	uiKVLine("Exécutable", choisi.rel+" (mémo du lanceur)")
}
