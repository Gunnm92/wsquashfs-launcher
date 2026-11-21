package savesync

// Synchro d'autres sauvegardes que celles des jeux Windows : dossiers déclarés (« custom »,
// --sync dir add) et sauvegardes des jeux Switch d'Eden, un dossier par jeu (title ID). Elles
// passent par les mêmes routes /v1/saves/*, le même --sync et la même règle que les jeux (un seul côté
// changé : il gagne ; les deux : conflit à départager dans le manager). Leur nom de synchro porte un préfixe :
//
//	dossier:<nom>     dossier déclaré sur la machine (chemin propre à chaque machine, sous HOME)
//	switch:<TitleID>  sauvegarde Eden d'un jeu Switch (16 chiffres hexadécimaux)
//
// Une machine où l'entrée n'existe pas (dossier non déclaré, Eden absent) ne la liste pas : elle
// est « absente » et n'est jamais touchée. Un jeu Windows est une cible « jeu » (users/ du prefix,
// profils Wine) ; les autres sont des dossiers simples, copiés tels quels.

import (
	"cmp"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"archive/tar"

	"wsquashfs-launcher/internal/filetree"
)

const (
	syncDirPrefix    = "dossier:"
	syncSwitchPrefix = "switch:"
)

// Dir : dossier déclaré dans sync.json.
type Dir struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

var (
	syncDirNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,47}$`)
	switchTitleRe = regexp.MustCompile(`^[0-9A-F]{16}$`)
	edenUserRe    = regexp.MustCompile(`^[0-9A-F]{32}$`)
)

var (
	ErrInvalid = errors.New("nom de synchro invalide")
	ErrAbsent  = errors.New("absent de cette machine")
)

// validSyncName : nom d'entrée de synchro : un jeu, dossier:<nom> ou switch:<TitleID>.
func validSyncName(name string) bool {
	switch {
	case strings.HasPrefix(name, syncDirPrefix):
		return syncDirNameRe.MatchString(strings.TrimPrefix(name, syncDirPrefix))
	case strings.HasPrefix(name, syncSwitchPrefix):
		return switchTitleRe.MatchString(strings.TrimPrefix(name, syncSwitchPrefix))
	}
	return ValidGame(name)
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
	real, err := filetree.UnderHome(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	home, err = filetree.Home()
	if err != nil {
		return "", filetree.ErrHomeInaccessible
	}
	if !filetree.StrictlyUnder(home, real) {
		return "", errors.New("le dossier doit être un sous-dossier de HOME (" + home + ")")
	}
	if st, err := os.Stat(real); err == nil && !st.IsDir() {
		return "", errors.New("pas un dossier : " + p)
	}
	return real, nil
}

// DirHeader : en-tête qui transporte le chemin d'un dossier déclaré (à partir de HOME, encodé en
// pourcentage : accents et espaces passent dans un en-tête HTTP). Une machine qui reçoit un dossier
// qu'elle n'a pas déclaré le déclare avec ce chemin, sous le même nom (comme un jeu installé ailleurs).
const DirHeader = "X-Wsq-Dir"

// relDir : chemin d'un dossier déclaré à partir de HOME, "" s'il est ailleurs (rien à transmettre).
func (t Target) RelDir() string {
	if !strings.HasPrefix(t.Name, syncDirPrefix) {
		return ""
	}
	if rel := HomeRel(t.Dir); rel != "" && !filepath.IsAbs(rel) {
		return rel
	}
	return ""
}

// maxDirRunes : longueur maximale d'un chemin de dossier déclaré, en caractères (comme le manager).
const maxDirRunes = 512

// checkDeclaredRel : règle du chemin d'un dossier déclaré reçu d'une autre machine (contract/cases/
// names.yaml, « dossier_paths », la même que le manager) : relatif à HOME, non vide, 512 caractères au
// plus, sans caractère de contrôle, sans espace en début ni en fin, sans « .. » (« / » ou « \ »
// séparateurs), et pas seulement « . » (la racine HOME n'est pas un dossier déclarable). Plus strict
// que checkSyncDirPath, qui accepte ce que l'utilisateur tape en local (chemin absolu, « ~/ »).
func checkDeclaredRel(rel string) error {
	if rel == "" || utf8.RuneCountInString(rel) > maxDirRunes {
		return fmt.Errorf("chemin invalide : %d caractères au plus", maxDirRunes)
	}
	if strings.TrimSpace(rel) != rel || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "~") {
		return fmt.Errorf("chemin invalide : %q (relatif à HOME, sans espace au bord)", rel)
	}
	for _, r := range rel {
		if r < 0x20 {
			return errors.New("chemin invalide : caractère de contrôle")
		}
	}
	parts := strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' })
	meaningful := false
	for _, part := range parts {
		if part == ".." {
			return fmt.Errorf("chemin invalide : %q (« .. » interdit)", rel)
		}
		if part != "." {
			meaningful = true
		}
	}
	if !meaningful {
		return fmt.Errorf("chemin invalide : %q (la racine HOME ne se déclare pas)", rel)
	}
	return nil
}

// DeclareFromPeer : déclare ici le dossier « dossier:<nom> » reçu d'une autre machine, avec le chemin
// qu'elle donne (à partir de HOME, vérifié par checkDeclaredRel puis par checkSyncDirPath). Rend la cible.
func (s *Syncer) DeclareFromPeer(name, rel string) (Target, error) {
	if !strings.HasPrefix(name, syncDirPrefix) || rel == "" {
		return Target{}, ErrAbsent
	}
	if err := checkDeclaredRel(rel); err != nil {
		return Target{}, err
	}
	cfg, err := AddDir(LoadConfig(), strings.TrimPrefix(name, syncDirPrefix), rel)
	if err != nil {
		return Target{}, err
	}
	if err := SaveConfig(cfg); err != nil {
		return Target{}, err
	}
	return s.TargetOf(name)
}

// HomeRel : chemin affiché à partir de HOME (« .local/share/x »), absolu s'il est ailleurs.
func HomeRel(p string) string {
	if p == "" {
		return ""
	}
	if home, err := filetree.Home(); err == nil && filetree.StrictlyUnder(home, p) {
		if rel, err := filepath.Rel(home, p); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return p
}

// AddDir : déclare (ou remplace, même nom) un dossier ; chemin vérifié.
func AddDir(cfg Config, name, path string) (Config, error) {
	if err := checkSyncDirName(name); err != nil {
		return cfg, err
	}
	real, err := checkSyncDirPath(path)
	if err != nil {
		return cfg, err
	}
	dirs := []Dir{}
	for _, d := range cfg.Dirs {
		if d.Name != name {
			dirs = append(dirs, d)
		}
	}
	dirs = append(dirs, Dir{Name: name, Path: real})
	slices.SortFunc(dirs, func(a, b Dir) int { return cmp.Compare(a.Name, b.Name) })
	cfg.Dirs = dirs
	return cfg, nil
}

// RemoveDir : retire la déclaration (les fichiers restent) ; false si le nom est inconnu.
func RemoveDir(cfg Config, name string) (Config, bool) {
	dirs := []Dir{}
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

// EdenDataDir : dossier de données d'Eden : WSQUASHFS_EDEN_DIR, sinon $XDG_DATA_HOME/eden
// (~/.local/share/eden), sinon celui du Flatpak. "" si aucun n'a de nand/.
func EdenDataDir() string {
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
	data := EdenDataDir()
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
	slices.Sort(out)
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
func (s *Syncer) edenTitleName(id string) string {
	data := EdenDataDir()
	if data == "" {
		return ""
	}
	if b, err := os.ReadFile(filepath.Join(edenCacheDir(data), "game_list", id+".appname.txt")); err == nil {
		if name := cleanTitleName(string(b)); name != "" {
			return name
		}
	}
	return s.switchRomNames()[id]
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
func (s *Syncer) switchRomDirs() []string {
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
	if s.env.AgentRoms != nil {
		for _, roms := range s.env.AgentRoms() {
			dirs = append(dirs, filepath.Join(roms, "switch"), filepath.Join(roms, "switch2"))
		}
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
func (s *Syncer) switchRomNames() map[string]string {
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
	dirs := s.switchRomDirs()
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

// edenRunning : un processus Eden tourne-t-il ? (comm commençant par « eden » ou « yuzu »).
func (s *Syncer) edenRunning() bool {
	if s.env.EdenRunning != nil {
		return s.env.EdenRunning()
	}
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

// processComm : nom du processus (comm), "" si illisible.
func processComm(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ── Cibles de synchro ───────────────────────────────────────────────────

// Target : ce qu'un nom de synchro désigne sur cette machine.
type Target struct {
	Name  string // nom de synchro (jeu, dossier:<nom>, switch:<ID>)
	Dir   string // users/ du jeu, ou le dossier lui-même
	Plain bool   // dossier simple : pas de profils Wine, tout le dossier est copié
	Label string // nom lisible : nom du dossier, nom du jeu Switch ("" pour un jeu Windows)
	game  bool
	s     *Syncer // pour le verrou et l'archivage d'un jeu (Env)
}

// GameTarget : cible d'un jeu Windows dont users/ est donné.
func (s *Syncer) GameTarget(name, users string) Target {
	return Target{Name: name, Dir: users, game: true, s: s}
}

// TargetOf : cible d'un nom sur cette machine. ErrInvalid : nom invalide ; ErrAbsent :
// dossier non déclaré, ou Eden absent (ou sans profil) pour un jeu Switch.
func (s *Syncer) TargetOf(name string) (Target, error) {
	if !validSyncName(name) {
		return Target{}, ErrInvalid
	}
	switch {
	case strings.HasPrefix(name, syncDirPrefix):
		dir := strings.TrimPrefix(name, syncDirPrefix)
		for _, d := range LoadConfig().Dirs {
			if d.Name == dir {
				return Target{Name: name, Dir: d.Path, Plain: true, Label: dir, s: s}, nil
			}
		}
		return Target{}, ErrAbsent
	case strings.HasPrefix(name, syncSwitchPrefix):
		id := strings.TrimPrefix(name, syncSwitchPrefix)
		user := edenSaveUser()
		if user == "" {
			return Target{}, ErrAbsent
		}
		label := s.edenTitleName(id)
		if label == "" {
			label = id
		}
		return Target{Name: name, Dir: filepath.Join(user, id), Plain: true, Label: "Switch · " + label, s: s}, nil
	}
	return s.GameTarget(name, s.UsersDir(name)), nil
}

// extraSyncNames : dossiers déclarés et jeux Switch d'Eden qui ont des sauvegardes, triés.
func (s *Syncer) extraSyncNames() []string {
	var out []string
	for _, d := range LoadConfig().Dirs {
		t := Target{Dir: d.Path, Plain: true}
		if t.Fingerprint(t.Files()) != "" {
			out = append(out, syncDirPrefix+d.Name)
		}
	}
	if user := edenSaveUser(); user != "" {
		for _, id := range edenTitlesOf(user) {
			t := Target{Dir: filepath.Join(user, id), Plain: true}
			if t.Fingerprint(t.Files()) != "" {
				out = append(out, syncSwitchPrefix+id)
			}
		}
	}
	slices.Sort(out)
	return out
}

// Names : tout ce que cette machine peut synchroniser (jeux, dossiers, jeux Switch), trié.
func (s *Syncer) Names() []string {
	out := append(s.syncGames(), s.extraSyncNames()...)
	slices.Sort(out)
	return out
}

// files : fichiers de la cible (périmètre de sauvegarde d'un jeu, ou tout le dossier).
func (t Target) Files() []File {
	if t.Plain {
		return collectPlainFiles(t.Dir)
	}
	return CollectSaveFiles(t.Dir)
}

// fingerprint : empreinte des fichiers ("" sans fichier).
func (t Target) Fingerprint(files []File) string {
	if t.Plain {
		return plainFingerprint(files)
	}
	return saveFingerprint(t.Dir, files)
}

// content : empreinte de contenu des fichiers ("" sans fichier ou si l'un est illisible).
func (t Target) Content(files []File) string {
	return contentFingerprint(t.Dir, files, !t.Plain)
}

// writeArchive : tar.gz des fichiers ; profil Wine unique d'un jeu (voir writeSaveArchive), "" sinon.
func (t Target) WriteArchive(w io.Writer, files []File) (string, error) {
	if t.Plain {
		return "", writeArchiveTree(w, t.Dir, files, func(rel string) string { return rel })
	}
	return writeSaveArchive(w, t.Dir, files)
}

// apply : remplace les fichiers de la cible par ceux de l'archive.
func (t Target) Apply(r io.Reader, profile string) error {
	if t.Plain {
		return applyPlainArchive(r, t.Dir)
	}
	return applySaveArchive(r, t.Dir, profile)
}

// lock : verrou pendant la synchro. Jeu Windows : verrou du jeu (refusé s'il tourne) ; jeu Switch :
// refusé si Eden tourne ; dossier déclaré : rien à vérifier.
func (t Target) Lock() (*os.File, bool) {
	switch {
	case t.game:
		if t.s == nil || t.s.env.LockIdleGame == nil {
			return nil, true
		}
		return t.s.env.LockIdleGame(t.Name)
	case strings.HasPrefix(t.Name, syncSwitchPrefix):
		return nil, !t.s.edenRunning()
	}
	return nil, true
}

// archiveLocal : met de côté le dossier de la cible dans le dossier des backups.
func (t Target) ArchiveLocal() (string, error) {
	if t.s == nil || t.s.env.ArchiveUsers == nil {
		return "", errors.New("archivage non disponible")
	}
	return t.s.env.ArchiveUsers(strings.ReplaceAll(t.Name, ":", "-"), filepath.Dir(t.Dir), filepath.Base(t.Dir))
}

// removeFiles : supprime les fichiers (puis les dossiers devenus vides, sans remonter au-delà du
// profil pour un jeu, du dossier pour un dossier simple). Retourne le nombre de fichiers supprimés.
func (t Target) RemoveFiles(files []File) (int, error) {
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
func collectPlainFiles(root string) []File {
	if st, err := os.Lstat(root); err != nil || !st.IsDir() {
		return nil
	}
	var out []File
	_ = filepath.Walk(root, func(f string, info os.FileInfo, err error) error {
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, f)
		if err != nil {
			return nil
		}
		out = append(out, File{Rel: filepath.ToSlash(rel), Size: info.Size(), MTime: info.ModTime().Unix()})
		return nil
	})
	slices.SortFunc(out, func(a, b File) int { return cmp.Compare(a.Rel, b.Rel) })
	return out
}

// plainFingerprint : empreinte d'un dossier simple (chemin, taille, date de chaque fichier).
func plainFingerprint(files []File) string {
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
	return replaceFromArchive(r, root, checkPlainEntry, func() []File { return collectPlainFiles(root) })
}
