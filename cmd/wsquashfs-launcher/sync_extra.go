package main

// Synchro d'autres sauvegardes que celles des jeux Windows : dossiers déclarés (« custom »,
// --sync dir add) et sauvegardes des jeux Switch d'Eden, un dossier par jeu (title ID). Elles
// passent par les mêmes routes /v1/saves/*, le même --sync et la même règle que les jeux (la plus
// lourde gagne, la plus récente à taille proche ; la perdante est archivée). Leur nom de synchro porte un préfixe :
//
//	dossier:<nom>     dossier déclaré sur la machine (chemin propre à chaque machine, sous HOME)
//	switch:<TitleID>  sauvegarde Eden d'un jeu Switch (16 chiffres hexadécimaux)
//
// Une machine où l'entrée n'existe pas (dossier non déclaré, Eden absent) ne la liste pas : elle
// est « absente » et n'est jamais touchée. Un jeu Windows est une cible « jeu » (users/ du prefix,
// profils Wine) ; les autres sont des dossiers simples, copiés tels quels.

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"archive/tar"
)

const (
	syncDirPrefix    = "dossier:"
	syncSwitchPrefix = "switch:"
)

// syncDir : dossier déclaré dans sync.json.
type syncDir struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

var (
	syncDirNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,47}$`)
	switchTitleRe = regexp.MustCompile(`^[0-9A-F]{16}$`)
	edenUserRe    = regexp.MustCompile(`^[0-9A-F]{32}$`)
)

var (
	errSyncInvalid = errors.New("nom de synchro invalide")
	errSyncAbsent  = errors.New("absent de cette machine")
)

// validSyncName : nom d'entrée de synchro : un jeu, dossier:<nom> ou switch:<TitleID>.
func validSyncName(name string) bool {
	switch {
	case strings.HasPrefix(name, syncDirPrefix):
		return syncDirNameRe.MatchString(strings.TrimPrefix(name, syncDirPrefix))
	case strings.HasPrefix(name, syncSwitchPrefix):
		return switchTitleRe.MatchString(strings.TrimPrefix(name, syncSwitchPrefix))
	}
	return validSyncGame(name)
}

// ── Dossiers déclarés ───────────────────────────────────────────────────

// checkSyncDirName : nom d'un dossier déclaré (lettres, chiffres, espace, _ et -, 48 au plus).
func checkSyncDirName(name string) error {
	if !syncDirNameRe.MatchString(name) {
		return fmt.Errorf("nom invalide : %q (lettres, chiffres, espace, _ et -, 48 caractères au plus)", name)
	}
	return nil
}

// checkSyncDirPath : chemin d'un dossier déclaré, donné à partir de HOME (« .local/share/x »,
// « ~/.local/share/x ») ou absolu ; il doit être sous HOME (HOME exclu), liens résolus sur la partie
// existante. Le dossier peut ne pas exister encore (il sera créé à la première réception).
// Retourne le chemin absolu avec le préfixe réel.
func checkSyncDirPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	home := os.Getenv("HOME")
	switch {
	case p == "" || p == "~":
		return "", errors.New("chemin requis, à partir du dossier personnel (ex. .local/share/jeu/save)")
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(home, p[2:])
	case !filepath.IsAbs(p):
		p = filepath.Join(home, p)
	}
	real, code, msg := gamesVerify(filepath.Clean(p))
	if code != 0 {
		return "", errors.New(msg)
	}
	home, err := gamesHome()
	if err != nil {
		return "", errors.New("HOME inaccessible")
	}
	if !gamesStrictlyUnder(home, real) {
		return "", errors.New("le dossier doit être un sous-dossier de HOME (" + home + ")")
	}
	if st, err := os.Stat(real); err == nil && !st.IsDir() {
		return "", errors.New("pas un dossier : " + p)
	}
	return real, nil
}

// syncDirHeader : en-tête qui transporte le chemin d'un dossier déclaré (à partir de HOME, encodé en
// pourcentage : accents et espaces passent dans un en-tête HTTP). Une machine qui reçoit un dossier
// qu'elle n'a pas déclaré le déclare avec ce chemin, sous le même nom (comme un jeu installé ailleurs).
const syncDirHeader = "X-Wsq-Dir"

// relDir : chemin d'un dossier déclaré à partir de HOME, "" s'il est ailleurs (rien à transmettre).
func (t syncTarget) relDir() string {
	if !strings.HasPrefix(t.Name, syncDirPrefix) {
		return ""
	}
	if rel := homeRel(t.Dir); rel != "" && !filepath.IsAbs(rel) {
		return rel
	}
	return ""
}

// declareFromPeer : déclare ici le dossier « dossier:<nom> » reçu d'une autre machine, avec le chemin
// qu'elle donne (à partir de HOME, vérifié par checkSyncDirPath). Rend la cible.
func declareFromPeer(name, rel string) (syncTarget, error) {
	if !strings.HasPrefix(name, syncDirPrefix) || rel == "" {
		return syncTarget{}, errSyncAbsent
	}
	cfg, err := addSyncDir(loadSyncConfig(), strings.TrimPrefix(name, syncDirPrefix), rel)
	if err != nil {
		return syncTarget{}, err
	}
	if err := saveSyncConfig(cfg); err != nil {
		return syncTarget{}, err
	}
	return syncTargetOf(name)
}

// homeRel : chemin affiché à partir de HOME (« .local/share/x »), absolu s'il est ailleurs.
func homeRel(p string) string {
	if p == "" {
		return ""
	}
	if home, err := gamesHome(); err == nil && gamesStrictlyUnder(home, p) {
		if rel, err := filepath.Rel(home, p); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return p
}

// addSyncDir : déclare (ou remplace, même nom) un dossier ; chemin vérifié.
func addSyncDir(cfg syncConfig, name, path string) (syncConfig, error) {
	if err := checkSyncDirName(name); err != nil {
		return cfg, err
	}
	real, err := checkSyncDirPath(path)
	if err != nil {
		return cfg, err
	}
	dirs := []syncDir{}
	for _, d := range cfg.Dirs {
		if d.Name != name {
			dirs = append(dirs, d)
		}
	}
	dirs = append(dirs, syncDir{Name: name, Path: real})
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	cfg.Dirs = dirs
	return cfg, nil
}

// removeSyncDir : retire la déclaration (les fichiers restent) ; false si le nom est inconnu.
func removeSyncDir(cfg syncConfig, name string) (syncConfig, bool) {
	dirs := []syncDir{}
	found := false
	for _, d := range cfg.Dirs {
		if d.Name == name {
			found = true
			continue
		}
		dirs = append(dirs, d)
	}
	cfg.Dirs = dirs
	return cfg, found
}

// ── Eden (Switch) ───────────────────────────────────────────────────────

// edenDataDir : dossier de données d'Eden : WSQUASHFS_EDEN_DIR, sinon $XDG_DATA_HOME/eden
// (~/.local/share/eden), sinon celui du Flatpak. "" si aucun n'a de nand/.
func edenDataDir() string {
	if v := os.Getenv("WSQUASHFS_EDEN_DIR"); v != "" {
		return v
	}
	home := os.Getenv("HOME")
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	for _, d := range []string{
		filepath.Join(data, "eden"),
		filepath.Join(home, ".var", "app", "dev.eden_emu.eden", "data", "eden"),
	} {
		if isDirPath(filepath.Join(d, "nand")) {
			return d
		}
	}
	return ""
}

// edenCacheDir : cache d'Eden (noms des jeux) : à côté des données pour WSQUASHFS_EDEN_DIR et le
// Flatpak, sinon $XDG_CACHE_HOME/eden (~/.cache/eden).
func edenCacheDir(data string) string {
	if os.Getenv("WSQUASHFS_EDEN_DIR") != "" || strings.Contains(data, "/.var/app/") {
		return filepath.Join(filepath.Dir(data), "cache", "eden")
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		cache = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(cache, "eden")
}

// edenSaveRoot : nand/user/save/0000000000000000 d'Eden ("" si Eden est absent).
func edenSaveRoot() string {
	data := edenDataDir()
	if data == "" {
		return ""
	}
	return filepath.Join(data, "nand", "user", "save", "0000000000000000")
}

// edenTitlesOf : title IDs (dossiers) d'un profil Eden, triés.
func edenTitlesOf(user string) []string {
	var out []string
	entries, _ := os.ReadDir(user)
	for _, e := range entries {
		if e.IsDir() && switchTitleRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// edenSaveUser : dossier du profil Eden qui porte les sauvegardes : celui qui a le plus de jeux
// (le premier par nom à égalité). Chaque machine a son propre identifiant de profil : les archives
// ne le contiennent pas. "" si Eden est absent ou n'a encore aucun profil (le lancer une fois).
func edenSaveUser() string {
	root := edenSaveRoot()
	if root == "" {
		return ""
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	best, bestN := "", -1
	for _, e := range entries {
		if !e.IsDir() || !edenUserRe.MatchString(e.Name()) {
			continue
		}
		if n := len(edenTitlesOf(filepath.Join(root, e.Name()))); n > bestN {
			best, bestN = filepath.Join(root, e.Name()), n
		}
	}
	return best
}

// edenTitleName : nom d'un jeu Switch : cache de la liste d'Eden (<cache>/game_list/<ID>.appname.txt,
// écrit par certaines versions seulement), sinon le nom du fichier de la ROM (« Nom [TitleID][v0].nsp »)
// dans les dossiers de jeux d'Eden ou le dossier switch/ des roms de l'agent. "" s'il est introuvable.
func edenTitleName(id string) string {
	data := edenDataDir()
	if data == "" {
		return ""
	}
	if b, err := os.ReadFile(filepath.Join(edenCacheDir(data), "game_list", id+".appname.txt")); err == nil {
		if name := cleanTitleName(string(b)); name != "" {
			return name
		}
	}
	return switchRomNames()[id]
}

// cleanTitleName : nom affichable (une ligne, 120 caractères au plus), "" sinon.
func cleanTitleName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 || strings.ContainsAny(name, "\x00\n\r") {
		return ""
	}
	return name
}

// romTitleRe : « Nom [0100F2C0115B6000]… » dans un nom de fichier de ROM Switch.
var romTitleRe = regexp.MustCompile(`^(.*?)\s*\[([0-9A-Fa-f]{16})\]`)

// romIDRe : premier « [TitleID] » d'un nom de fichier.
var romIDRe = regexp.MustCompile(`\[([0-9A-Fa-f]{16})\]`)

// switchRomDirs : dossiers de ROM Switch : dossiers de jeux d'Eden (qt-config.ini, chemins absolus),
// puis switch/ et switch2/ de chaque dossier roms/ de l'agent.
func switchRomDirs() []string {
	var dirs []string
	base := configHome()
	for _, ini := range []string{
		filepath.Join(base, "eden", "qt-config.ini"),
		filepath.Join(os.Getenv("HOME"), ".var", "app", "dev.eden_emu.eden", "config", "eden", "qt-config.ini"),
	} {
		data, err := os.ReadFile(ini)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if ok && strings.HasPrefix(key, `Paths\gamedirs\`) && strings.HasSuffix(key, `\path`) && filepath.IsAbs(value) {
				dirs = append(dirs, value)
			}
		}
	}
	for _, roms := range loadAgentConfig().Roms {
		dirs = append(dirs, filepath.Join(roms, "switch"), filepath.Join(roms, "switch2"))
	}
	return dirs
}

var (
	romNamesMu sync.Mutex
	romNamesAt time.Time
	romNames   map[string]string
)

// switchRomNames : TitleID → nom, d'après les dossiers de ROM (gardé 30 s : un appel par jeu Switch
// pendant une liste) : d'abord gamelist.xml (nom de la fiche ; TitleID dans <path>, qui le garde même
// après un renommage du fichier), puis les noms de fichiers. Une ROM de mise à jour (…800) nomme
// aussi son jeu de base (…000).
func switchRomNames() map[string]string {
	romNamesMu.Lock()
	defer romNamesMu.Unlock()
	if romNames != nil && time.Since(romNamesAt) < 30*time.Second {
		return romNames
	}
	names := map[string]string{}
	add := func(rawID, rawName string) {
		name := cleanTitleName(rawName)
		if name == "" {
			return
		}
		id := strings.ToUpper(rawID)
		for _, k := range []string{id, id[:13] + "000"} {
			if _, ok := names[k]; !ok {
				names[k] = name
			}
		}
	}
	dirs := switchRomDirs()
	for _, dir := range dirs {
		var list struct {
			Games []struct {
				Path string `xml:"path"`
				Name string `xml:"name"`
			} `xml:"game"`
		}
		if data, err := os.ReadFile(filepath.Join(dir, "gamelist.xml")); err == nil && xml.Unmarshal(data, &list) == nil {
			for _, g := range list.Games {
				if m := romIDRe.FindStringSubmatch(filepath.Base(g.Path)); m != nil {
					add(m[1], g.Name)
				}
			}
		}
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if m := romTitleRe.FindStringSubmatch(e.Name()); m != nil {
				add(m[2], m[1])
			}
		}
	}
	romNames, romNamesAt = names, time.Now()
	return names
}

// edenRunningCheck : un processus Eden tourne-t-il ? (comm commençant par « eden » ou « yuzu »).
// Variable pour les tests.
var edenRunningCheck = func() bool {
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid := 0
		if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err != nil || pid <= 0 {
			continue
		}
		comm := strings.ToLower(processComm(pid))
		if strings.HasPrefix(comm, "eden") || strings.HasPrefix(comm, "yuzu") {
			return true
		}
	}
	return false
}

// ── Cibles de synchro ───────────────────────────────────────────────────

// syncTarget : ce qu'un nom de synchro désigne sur cette machine.
type syncTarget struct {
	Name  string // nom de synchro (jeu, dossier:<nom>, switch:<ID>)
	Dir   string // users/ du jeu, ou le dossier lui-même
	Plain bool   // dossier simple : pas de profils Wine, tout le dossier est copié
	Label string // nom lisible : nom du dossier, nom du jeu Switch ("" pour un jeu Windows)
	game  bool
}

// gameTarget : cible d'un jeu Windows dont users/ est donné.
func gameTarget(name, users string) syncTarget {
	return syncTarget{Name: name, Dir: users, game: true}
}

// syncTargetOf : cible d'un nom sur cette machine. errSyncInvalid : nom invalide ; errSyncAbsent :
// dossier non déclaré, ou Eden absent (ou sans profil) pour un jeu Switch.
func syncTargetOf(name string) (syncTarget, error) {
	if !validSyncName(name) {
		return syncTarget{}, errSyncInvalid
	}
	switch {
	case strings.HasPrefix(name, syncDirPrefix):
		dir := strings.TrimPrefix(name, syncDirPrefix)
		for _, d := range loadSyncConfig().Dirs {
			if d.Name == dir {
				return syncTarget{Name: name, Dir: d.Path, Plain: true, Label: dir}, nil
			}
		}
		return syncTarget{}, errSyncAbsent
	case strings.HasPrefix(name, syncSwitchPrefix):
		id := strings.TrimPrefix(name, syncSwitchPrefix)
		user := edenSaveUser()
		if user == "" {
			return syncTarget{}, errSyncAbsent
		}
		label := edenTitleName(id)
		if label == "" {
			label = id
		}
		return syncTarget{Name: name, Dir: filepath.Join(user, id), Plain: true, Label: "Switch · " + label}, nil
	}
	return gameTarget(name, syncUsersDir(name)), nil
}

// syncTargetHTTP : code HTTP d'une erreur de syncTargetOf.
func syncTargetHTTP(err error) (int, string) {
	if errors.Is(err, errSyncAbsent) {
		return http.StatusNotFound, "absent de cette machine (dossier non déclaré, ou Eden absent)"
	}
	return http.StatusBadRequest, "jeu invalide"
}

// extraSyncNames : dossiers déclarés et jeux Switch d'Eden qui ont des sauvegardes, triés.
func extraSyncNames() []string {
	var out []string
	for _, d := range loadSyncConfig().Dirs {
		t := syncTarget{Dir: d.Path, Plain: true}
		if t.fingerprint(t.files()) != "" {
			out = append(out, syncDirPrefix+d.Name)
		}
	}
	if user := edenSaveUser(); user != "" {
		for _, id := range edenTitlesOf(user) {
			t := syncTarget{Dir: filepath.Join(user, id), Plain: true}
			if t.fingerprint(t.files()) != "" {
				out = append(out, syncSwitchPrefix+id)
			}
		}
	}
	sort.Strings(out)
	return out
}

// syncNames : tout ce que cette machine peut synchroniser (jeux, dossiers, jeux Switch), trié.
func syncNames() []string {
	out := append(syncGames(), extraSyncNames()...)
	sort.Strings(out)
	return out
}

// files : fichiers de la cible (périmètre de sauvegarde d'un jeu, ou tout le dossier).
func (t syncTarget) files() []saveFile {
	if t.Plain {
		return collectPlainFiles(t.Dir)
	}
	return collectSaveFiles(t.Dir)
}

// fingerprint : empreinte des fichiers ("" sans fichier).
func (t syncTarget) fingerprint(files []saveFile) string {
	if t.Plain {
		return plainFingerprint(files)
	}
	return saveFingerprint(t.Dir, files)
}

// content : empreinte de contenu des fichiers ("" sans fichier ou si l'un est illisible).
func (t syncTarget) content(files []saveFile) string {
	return contentFingerprint(t.Dir, files, !t.Plain)
}

// writeArchive : tar.gz des fichiers ; profil Wine unique d'un jeu (voir writeSaveArchive), "" sinon.
func (t syncTarget) writeArchive(w io.Writer, files []saveFile) (string, error) {
	if t.Plain {
		return "", writeArchiveTree(w, t.Dir, files, func(rel string) string { return rel })
	}
	return writeSaveArchive(w, t.Dir, files)
}

// apply : remplace les fichiers de la cible par ceux de l'archive.
func (t syncTarget) apply(r io.Reader, profile string) error {
	if t.Plain {
		return applyPlainArchive(r, t.Dir)
	}
	return applySaveArchive(r, t.Dir, profile)
}

// lock : verrou pendant la synchro. Jeu Windows : verrou du jeu (refusé s'il tourne) ; jeu Switch :
// refusé si Eden tourne ; dossier déclaré : rien à vérifier.
func (t syncTarget) lock() (*os.File, bool) {
	switch {
	case t.game:
		return lockIdleGame(t.Name)
	case strings.HasPrefix(t.Name, syncSwitchPrefix):
		return nil, !edenRunningCheck()
	}
	return nil, true
}

// archiveLocal : met de côté le dossier de la cible dans le dossier des backups.
func (t syncTarget) archiveLocal() (string, error) {
	return archiveUsers(strings.ReplaceAll(t.Name, ":", "-"), filepath.Dir(t.Dir), filepath.Base(t.Dir))
}

// removeFiles : supprime les fichiers (puis les dossiers devenus vides, sans remonter au-delà du
// profil pour un jeu, du dossier pour un dossier simple). Retourne le nombre de fichiers supprimés.
func (t syncTarget) removeFiles(files []saveFile) (int, error) {
	removed := 0
	for _, f := range files {
		stop := t.Dir
		if !t.Plain {
			profile, _ := splitRel(f.Rel)
			stop = filepath.Join(t.Dir, profile)
		}
		p := filepath.Join(t.Dir, filepath.FromSlash(f.Rel))
		switch err := os.Remove(p); {
		case err == nil:
			removed++
		case !errors.Is(err, fs.ErrNotExist):
			return removed, err
		}
		removeEmptyDirs(filepath.Dir(p), stop)
	}
	return removed, nil
}

// ── Dossiers simples ────────────────────────────────────────────────────

// collectPlainFiles : tous les fichiers réguliers d'un dossier, triés par Rel (relatif au dossier).
// Dossier absent, ou lien : nil. Les liens ne sont jamais suivis.
func collectPlainFiles(root string) []saveFile {
	if st, err := os.Lstat(root); err != nil || !st.IsDir() {
		return nil
	}
	var out []saveFile
	_ = filepath.Walk(root, func(f string, info os.FileInfo, err error) error {
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, f)
		if err != nil {
			return nil
		}
		out = append(out, saveFile{Rel: filepath.ToSlash(rel), Size: info.Size(), MTime: info.ModTime().Unix()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out
}

// plainFingerprint : empreinte d'un dossier simple (chemin, taille, date de chaque fichier).
func plainFingerprint(files []saveFile) string {
	if len(files) == 0 {
		return ""
	}
	lines := make([]string, 0, len(files))
	for _, f := range files {
		lines = append(lines, fmt.Sprintf("%s\x00%d\x00%d\n", f.Rel, f.Size, f.MTime))
	}
	return hashLines(lines)
}

// checkPlainEntry : entrée d'archive d'un dossier simple : fichier ou dossier, chemin relatif sans
// « .. » ni composant vide. Retourne le chemin relatif.
func checkPlainEntry(hdr *tar.Header) (string, bool, error) {
	isDir := false
	switch hdr.Typeflag {
	case tar.TypeReg:
	case tar.TypeDir:
		isDir = true
	default:
		return "", false, fmt.Errorf("entrée refusée (type non autorisé) : %q", hdr.Name)
	}
	name := strings.TrimSuffix(hdr.Name, "/")
	if name == "" || strings.HasPrefix(name, "/") {
		return "", false, fmt.Errorf("entrée refusée (chemin invalide) : %q", hdr.Name)
	}
	for _, p := range strings.Split(name, "/") {
		if p == "" || p == "." || p == ".." {
			return "", false, fmt.Errorf("entrée refusée (chemin invalide) : %q", hdr.Name)
		}
	}
	return name, isDir, nil
}

// applyPlainArchive : remplace le contenu d'un dossier simple par celui de l'archive (même garanties
// que applySaveArchive : rien n'est modifié si l'archive est refusée).
func applyPlainArchive(r io.Reader, root string) error {
	return replaceFromArchive(r, root, checkPlainEntry, func() []saveFile { return collectPlainFiles(root) })
}
