package main

// Synchronisation manuelle des sauvegardes de partie entre machines (--sync) :
// le cœur local, sans réseau. Le périmètre est celui de --save (copyGameSaves) :
// sous users/, les profils hors comptes système, seulement les sous-dossiers
// saveProfileDirs, sans les caches. Les fonctions prennent des chemins explicites
// pour être testées en simulant deux machines dans un même test.

import (
	"archive/tar"
	"cmp"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
)

// syncProfileToken : nom neutre du profil dans les archives quand la source n'a qu'un profil
// (le profil Wine change d'une machine à l'autre : steamuser, deck, arcade...).
const syncProfileToken = "_profil_"

// syncDefaultProfile : profil Wine supposé quand la machine n'en a aucun et que l'envoi n'en précise pas.
const syncDefaultProfile = "steamuser"

// isSystemProfile : comptes Windows système, jamais synchronisés.
func isSystemProfile(name string) bool {
	switch name {
	case "Public", "All Users", "Default User", "LocalService", "NetworkService":
		return true
	}
	return false
}

// userProfiles : profils (dossiers) de users/ hors comptes système, triés.
// Un lien vers un dossier n'est pas un profil.
func userProfiles(users string) []string {
	entries, err := os.ReadDir(users)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !isSystemProfile(e.Name()) {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out
}

// saveFile : une sauvegarde du périmètre. Rel est relatif à users/, avec "/" et le profil réel en tête.
// MTime en secondes Unix.
type saveFile struct {
	Rel   string
	Size  int64
	MTime int64
}

// splitRel : profil et chemin restant d'un Rel ("deck", "Documents/x.sav").
func splitRel(rel string) (profile, rest string) {
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		return rel[:i], rel[i+1:]
	}
	return rel, ""
}

// collectSaveFiles : fichiers du périmètre de sauvegarde, triés par Rel. Dossier absent : nil.
// Les liens ne sont jamais suivis (Lstat, Walk ne descend pas dans les liens).
func collectSaveFiles(users string) []saveFile {
	var out []saveFile
	for _, p := range userProfiles(users) {
		for _, sub := range saveProfileDirs {
			src := filepath.Join(users, p, filepath.FromSlash(sub))
			if st, err := os.Lstat(src); err != nil || !st.IsDir() {
				continue
			}
			_ = filepath.Walk(src, func(f string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if info.IsDir() && isCacheDirName(info.Name()) {
					return filepath.SkipDir
				}
				if !info.Mode().IsRegular() || strings.HasPrefix(info.Name(), ".wh.") {
					return nil
				}
				rel, err := filepath.Rel(users, f)
				if err != nil {
					return nil
				}
				out = append(out, saveFile{
					Rel:   filepath.ToSlash(rel),
					Size:  info.Size(),
					MTime: info.ModTime().Unix(),
				})
				return nil
			})
		}
	}
	slices.SortFunc(out, func(a, b saveFile) int { return cmp.Compare(a.Rel, b.Rel) })
	return out
}

// fileProfiles : profils réels ayant au moins un fichier dans files, triés. Un profil vide n'y figure pas.
func fileProfiles(files []saveFile) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		p, _ := splitRel(f.Rel)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// saveFingerprint : empreinte du périmètre (sha256 hex) ; "" sans fichier. Quand les fichiers ne
// viennent que d'un profil, celui-ci est remplacé par syncProfileToken : deux machines aux mêmes
// fichiers ont la même empreinte, même si l'une a un profil vide en plus.
func saveFingerprint(users string, files []saveFile) string {
	if len(files) == 0 {
		return ""
	}
	single := len(fileProfiles(files)) == 1
	lines := make([]string, 0, len(files))
	for _, f := range files {
		rel := f.Rel
		if single {
			_, rest := splitRel(rel)
			rel = syncProfileToken + "/" + rest
		}
		lines = append(lines, fmt.Sprintf("%s\x00%d\x00%d\n", rel, f.Size, f.MTime))
	}
	return hashLines(lines)
}

// contentKey : clé du cache des hash de fichiers (chemin absolu, taille, date).
type contentKey struct {
	path  string
	size  int64
	mtime int64
}

// contentCacheMax : nombre d'entrées au-delà duquel le cache des hash est vidé.
const contentCacheMax = 20000

var (
	contentCacheMu sync.Mutex
	contentCache   = map[contentKey]string{}
)

// fileContentHash : sha256 hex du contenu d'un fichier, mis en cache tant que sa taille et sa date
// ne changent pas (pas de relecture à chaque listing). ok est faux si le fichier est illisible.
func fileContentHash(path string, size, mtime int64) (string, bool) {
	key := contentKey{path, size, mtime}
	contentCacheMu.Lock()
	sum, hit := contentCache[key]
	contentCacheMu.Unlock()
	if hit {
		return sum, true
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false
	}
	sum = hex.EncodeToString(h.Sum(nil))
	contentCacheMu.Lock()
	if len(contentCache) >= contentCacheMax {
		contentCache = map[contentKey]string{}
	}
	contentCache[key] = sum
	contentCacheMu.Unlock()
	return sum, true
}

// contentFingerprint : empreinte du contenu (sha256 hex des lignes triées « chemin \x00 sha256 du
// fichier \n »), sans tenir compte des dates. root : dossier des fichiers ; game : cible de jeu, dont
// le profil unique est remplacé par syncProfileToken comme dans saveFingerprint. "" sans fichier,
// ou si un fichier est illisible (on retombe alors sur l'empreinte habituelle).
func contentFingerprint(root string, files []saveFile, game bool) string {
	if len(files) == 0 {
		return ""
	}
	single := game && len(fileProfiles(files)) == 1
	lines := make([]string, 0, len(files))
	for _, f := range files {
		sum, ok := fileContentHash(filepath.Join(root, filepath.FromSlash(f.Rel)), f.Size, f.MTime)
		if !ok {
			return ""
		}
		rel := f.Rel
		if single {
			_, rest := splitRel(rel)
			rel = syncProfileToken + "/" + rest
		}
		lines = append(lines, rel+"\x00"+sum+"\n")
	}
	return hashLines(lines)
}

// hashLines : sha256 hexadécimal des lignes, triées.
func hashLines(lines []string) string {
	slices.Sort(lines)
	h := sha256.New()
	for _, l := range lines {
		_, _ = io.WriteString(h, l)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// newestSave : plus grand MTime du périmètre, 0 si vide.
func newestSave(files []saveFile) int64 {
	var newest int64
	for _, f := range files {
		if f.MTime > newest {
			newest = f.MTime
		}
	}
	return newest
}

// saveSize : taille totale des sauvegardes du périmètre, en octets.
func saveSize(files []saveFile) int64 {
	var total int64
	for _, f := range files {
		total += f.Size
	}
	return total
}

// archiveEntryName : nom d'une entrée d'archive ; le profil devient le token s'il est seul.
func archiveEntryName(rel string, single bool) string {
	if !single {
		return rel
	}
	_, rest := splitRel(rel)
	if rest == "" {
		return syncProfileToken
	}
	return syncProfileToken + "/" + rest
}

// writeSaveArchive : tar.gz des fichiers (entrées "<profil ou token>/<chemin>", dossiers
// intermédiaires compris, avec mode et date). Retourne le nom réel du profil quand il est seul
// (le token a été utilisé), "" sinon.
func writeSaveArchive(w io.Writer, users string, files []saveFile) (profile string, err error) {
	profs := fileProfiles(files)
	single := len(profs) == 1
	if single {
		profile = profs[0]
	}
	if err := writeArchiveTree(w, users, files, func(rel string) string { return archiveEntryName(rel, single) }); err != nil {
		return "", err
	}
	return profile, nil
}

// writeArchiveTree : tar.gz des fichiers de root (Rel relatifs à root), dossiers intermédiaires
// compris, avec mode et date ; name donne le nom d'entrée d'un chemin relatif.
func writeArchiveTree(w io.Writer, root string, files []saveFile, name func(rel string) string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	dirsDone := map[string]bool{}
	for _, f := range files {
		// Dossiers intermédiaires, une fois chacun, avant leurs fichiers.
		parts := strings.Split(f.Rel, "/")
		for i := 1; i < len(parts); i++ {
			dirRel := strings.Join(parts[:i], "/")
			if dirsDone[dirRel] {
				continue
			}
			dirsDone[dirRel] = true
			st, err := os.Lstat(filepath.Join(root, filepath.FromSlash(dirRel)))
			if err != nil {
				return err
			}
			hdr := &tar.Header{
				Typeflag: tar.TypeDir,
				Name:     name(dirRel) + "/",
				Mode:     int64(st.Mode().Perm()),
				ModTime:  time.Unix(st.ModTime().Unix(), 0),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
		}
		fp := filepath.Join(root, filepath.FromSlash(f.Rel))
		st, err := os.Lstat(fp)
		if err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("%s : fichier absent ou non régulier", f.Rel)
		}
		src, err := os.Open(fp)
		if err != nil {
			return err
		}
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name(f.Rel),
			Mode:     int64(st.Mode().Perm()),
			Size:     st.Size(),
			ModTime:  time.Unix(st.ModTime().Unix(), 0),
		}
		err = tw.WriteHeader(hdr)
		if err == nil {
			_, err = io.CopyN(tw, src, st.Size())
		}
		_ = src.Close()
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// syncEntry : entrée d'archive acceptée : profil (token non résolu), chemin restant, dossier ou fichier.
type syncEntry struct {
	profile string
	rest    string
	isDir   bool
}

// checkSyncEntry : refuse ce que l'archive ne peut pas contenir (chemins absolus ou "..", liens,
// types spéciaux, profil système, hors d'un sous-dossier saveProfileDirs, caches, .wh.).
func checkSyncEntry(hdr *tar.Header) (syncEntry, error) {
	refuse := func(why string) (syncEntry, error) {
		return syncEntry{}, fmt.Errorf("entrée refusée (%s) : %q", why, hdr.Name)
	}
	var e syncEntry
	switch hdr.Typeflag {
	case tar.TypeReg:
	case tar.TypeDir:
		e.isDir = true
	default:
		return refuse("type non autorisé")
	}
	name := hdr.Name
	if e.isDir {
		name = strings.TrimSuffix(name, "/")
	}
	if strings.HasPrefix(name, "/") {
		return refuse("chemin absolu")
	}
	parts := strings.Split(name, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return refuse("chemin invalide")
		}
	}
	e.profile = parts[0]
	if e.profile != syncProfileToken && isSystemProfile(e.profile) {
		return refuse("profil système")
	}
	if len(parts) == 1 {
		if !e.isDir {
			return refuse("fichier hors d'un profil")
		}
		return e, nil
	}
	sub := parts[1:]
	e.rest = strings.Join(sub, "/")
	// Chaque composant du chemin : un dossier intermédiaire doit mener à un saveProfileDirs,
	// un fichier doit être dedans.
	inside, toward := false, false
	for _, sd := range saveProfileDirs {
		if strings.HasPrefix(e.rest, sd+"/") || (e.isDir && e.rest == sd) {
			inside = true
		}
		if e.isDir && strings.HasPrefix(sd, e.rest+"/") {
			toward = true
		}
	}
	if !inside && !(e.isDir && toward) {
		return refuse("hors des sous-dossiers de sauvegarde")
	}
	dirParts := sub
	if !e.isDir {
		dirParts = sub[:len(sub)-1]
		if strings.HasPrefix(sub[len(sub)-1], ".wh.") {
			return refuse("fichier .wh.")
		}
	}
	for _, c := range dirParts {
		if isCacheDirName(c) {
			return refuse("dossier de cache")
		}
	}
	return e, nil
}

// validSyncProfileName : nom de profil local acceptable (pas système, pas de séparateur).
func validSyncProfileName(name string) bool {
	return name != "" && name != "." && name != ".." && name != syncProfileToken &&
		!strings.ContainsAny(name, "/\x00") && !isSystemProfile(name)
}

// localSyncProfile : profil local qui reçoit le token. Préférence : le profil unique portant des
// sauvegardes, sinon le profil unique de users/ (même vide), sinon l'envoyeur (origProfile), sinon
// syncDefaultProfile.
func localSyncProfile(users, origProfile string) (string, error) {
	if withSaves := fileProfiles(collectSaveFiles(users)); len(withSaves) == 1 {
		return withSaves[0], nil
	}
	if profs := userProfiles(users); len(profs) == 1 {
		return profs[0], nil
	}
	if origProfile == "" {
		return syncDefaultProfile, nil
	}
	if !validSyncProfileName(origProfile) {
		return "", fmt.Errorf("profil d'origine refusé : %q", origProfile)
	}
	return origProfile, nil
}

// syncMove : fichier extrait à déplacer à sa place dans users/.
type syncMove struct {
	src, dst string
	mtime    int64
}

// checkSyncDests : users/ et chaque composant existant entre users/ et une destination doivent être
// de vrais dossiers (ou le fichier final absent ou régulier), jamais des liens. Lecture seule.
func checkSyncDests(users string, moves []syncMove) error {
	if st, err := os.Lstat(users); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s est un lien, synchro refusée pour ce jeu", users)
	}
	for _, m := range moves {
		rel, err := filepath.Rel(users, m.dst)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		cur := users
		for i, p := range parts {
			cur = filepath.Join(cur, p)
			st, err := os.Lstat(cur)
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				return err
			}
			if st.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s est un lien, synchro refusée pour ce jeu", cur)
			}
			if i < len(parts)-1 && !st.IsDir() {
				return fmt.Errorf("%s n'est pas un dossier, synchro refusée pour ce jeu", cur)
			}
		}
	}
	return nil
}

// syncMaxUnpacked : taille décompressée maximale d'une archive reçue (somme des fichiers). Au-delà,
// l'extraction s'arrête et rien n'est modifié. Variable pour les tests.
var syncMaxUnpacked int64 = 4 << 30

// syncTempMaxAge : âge au-delà duquel une archive temporaire de synchro est un reste (processus tué) :
// --clean la retire.
const syncTempMaxAge = 24 * time.Hour

// syncTempDir : dossier des archives temporaires de synchro, dans le cache du lanceur (sur disque).
func syncTempDir() string {
	return filepath.Join(conf.cacheDir, "tmp")
}

// syncTempFile : fichier temporaire d'une archive de synchro (envoyée par l'agent, reçue par --sync).
// Dans le cache du lanceur et non dans /tmp, souvent en mémoire : une grosse sauvegarde y prendrait
// la RAM. Cache inconnu ou non inscriptible : dossier temporaire du système.
func syncTempFile(pattern string) (*os.File, error) {
	if conf.cacheDir != "" {
		if err := os.MkdirAll(syncTempDir(), 0o700); err == nil {
			if f, err := os.CreateTemp(syncTempDir(), pattern); err == nil {
				return f, nil
			}
		}
	}
	return os.CreateTemp("", pattern)
}

// pruneSyncTemp : retire les archives temporaires de synchro laissées par un processus tué (plus
// vieilles que syncTempMaxAge : une synchro en cours n'est pas touchée). Retourne la place libérée.
func pruneSyncTemp() (freed int64) {
	entries, _ := os.ReadDir(syncTempDir())
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) < syncTempMaxAge {
			continue
		}
		if os.Remove(filepath.Join(syncTempDir(), e.Name())) == nil {
			freed += info.Size()
		}
	}
	return freed
}

// archiveFileMode : droits d'un fichier extrait d'une archive de synchro : ceux de l'en-tête (bits
// de permission seulement), lisible et inscriptible par son propriétaire au moins ; 644 si l'en-tête
// n'en donne pas.
func archiveFileMode(hdr *tar.Header) os.FileMode {
	mode := os.FileMode(hdr.Mode).Perm()
	if mode == 0 {
		return 0o644
	}
	return mode | 0o600
}

// applySaveArchive : remplace le périmètre de users/ par le contenu de l'archive. Tout est extrait
// d'abord dans un dossier temporaire frère : une archive refusée ne modifie rien. Seuls les fichiers du
// périmètre actuel sont retirés ; le reste de users/ n'est jamais touché. Le jeu doit être arrêté.
// Refusées avant toute modification : une archive sans fichier (« archive vide »), une archive qui
// dépasse syncMaxUnpacked décompressés.
func applySaveArchive(r io.Reader, users string, origProfile string) error {
	local, err := localSyncProfile(users, origProfile)
	if err != nil {
		return err
	}
	entry := func(hdr *tar.Header) (string, bool, error) {
		e, err := checkSyncEntry(hdr)
		if err != nil {
			return "", false, err
		}
		profile := e.profile
		if profile == syncProfileToken {
			profile = local
		}
		rel := profile
		if e.rest != "" {
			rel += "/" + e.rest
		}
		return rel, e.isDir, nil
	}
	return replaceFromArchive(r, users, entry, func() []saveFile { return collectSaveFiles(users) })
}

// replaceFromArchive : extrait l'archive dans un dossier temporaire frère de root (entry valide chaque
// entrée et donne son chemin relatif à root), puis remplace les fichiers current() de root par ceux de
// l'archive. Rien n'est modifié tant que toute l'archive n'est pas extraite et vérifiée.
func replaceFromArchive(r io.Reader, root string, entry func(*tar.Header) (string, bool, error), current func() []saveFile) error {
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".wsq-sync-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()

	var moves []syncMove
	var unpacked int64
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		rel, isDir, err := entry(hdr)
		if err != nil {
			return err
		}
		// Taille annoncée par l'en-tête : une archive truquée ne fait pas écrire plus que la limite.
		if !isDir {
			unpacked += hdr.Size
			if unpacked > syncMaxUnpacked {
				return fmt.Errorf("archive trop volumineuse : plus de %s décompressés", sizeText(syncMaxUnpacked))
			}
		}
		p := filepath.Join(tmp, filepath.FromSlash(rel))
		if isDir {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		mode := archiveFileMode(hdr)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, cerr := io.CopyN(f, tr, hdr.Size)
		if err := f.Close(); cerr == nil {
			cerr = err
		}
		// Droits de l'archive tels quels (la création les a filtrés par l'umask).
		if cerr == nil {
			cerr = os.Chmod(p, mode)
		}
		if cerr != nil {
			return cerr
		}
		moves = append(moves, syncMove{
			src:   p,
			dst:   filepath.Join(root, filepath.FromSlash(rel)),
			mtime: hdr.ModTime.Unix(),
		})
	}

	// Une archive sans fichier effacerait le périmètre local : refusée.
	if len(moves) == 0 {
		return errors.New("archive vide")
	}
	// Tout a été extrait sans erreur, et rien n'est encore modifié : les destinations ne doivent pas
	// passer par un lien (MkdirAll et Rename suivraient le lien et écriraient hors de root).
	if err := checkSyncDests(root, moves); err != nil {
		return err
	}
	// On remplace le périmètre.
	for _, f := range current() {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(f.Rel))); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	for _, m := range moves {
		if err := os.MkdirAll(filepath.Dir(m.dst), 0o755); err != nil {
			return err
		}
		if err := os.Rename(m.src, m.dst); err != nil {
			return err
		}
		t := time.Unix(m.mtime, 0)
		if err := os.Chtimes(m.dst, t, t); err != nil {
			return err
		}
	}
	return nil
}

// syncState : état de la dernière synchro avec une machine distante, pour un jeu.
type syncState struct {
	Fingerprint string    `json:"fingerprint"`
	At          time.Time `json:"at"`
}

var syncIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// validSyncID : identifiant de machine, sûr dans un nom de fichier.
func validSyncID(id string) bool {
	return syncIDRe.MatchString(id)
}

// validSyncGame : nom de jeu tel que la ludothèque le connaît (pas de chemin, pas de caché).
func validSyncGame(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\x00") && !strings.HasPrefix(name, ".") &&
		gameNameOf(name) == name
}

// syncStatePath : <stateRoot>/<peerID>/<jeu>.json
func syncStatePath(stateRoot, peerID, game string) string {
	return filepath.Join(stateRoot, peerID, game+".json")
}

// loadSyncState : état enregistré ; absent, illisible ou identifiant invalide : état zéro.
func loadSyncState(stateRoot, peerID, game string) syncState {
	if !validSyncID(peerID) || !validSyncGame(game) {
		return syncState{}
	}
	data, err := os.ReadFile(syncStatePath(stateRoot, peerID, game))
	if err != nil {
		return syncState{}
	}
	var st syncState
	if err := json.Unmarshal(data, &st); err != nil {
		return syncState{}
	}
	return st
}

// saveSyncState : enregistre l'empreinte après une synchro, par écriture atomique.
func saveSyncState(stateRoot, peerID, game, fp string) error {
	if !validSyncID(peerID) || !validSyncGame(game) {
		return fmt.Errorf("état de synchro refusé : %q / %q", peerID, game)
	}
	path := syncStatePath(stateRoot, peerID, game)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(syncState{Fingerprint: fp, At: time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// removeSyncState : efface l'état de synchro de cette machine pour ce jeu ; absent : rien à faire.
// Les états des autres machines ne sont pas touchés.
func removeSyncState(stateRoot, peerID, game string) error {
	if !validSyncID(peerID) || !validSyncGame(game) {
		return fmt.Errorf("état de synchro refusé : %q / %q", peerID, game)
	}
	if err := os.Remove(syncStatePath(stateRoot, peerID, game)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// syncAction : ce que la synchro doit faire pour un jeu.
type syncAction int

const (
	syncUpToDate syncAction = iota
	syncSend
	syncReceive
	syncConflict // les deux ont changé : rien n'est écrasé, l'utilisateur départage dans le manager
)

// decideSync : sens de la synchro (règle d'Unison, comme le manager), d'après les empreintes locale
// et distante et l'état de la dernière synchro (chacune son empreinte) :
//   - même empreinte, ou mêmes contenus non vides : à jour ;
//   - un côté n'a rien : l'autre est copié ;
//   - un seul côté a changé depuis son état : il gagne (envoi ou réception) ;
//   - état inconnu des deux côtés (première synchro) : la copie la plus récente gagne, à égalité
//     de date (localNewest, remoteNewest : MTime le plus récent, en s) c'est un conflit ;
//   - les deux ont changé : conflit, jamais tranché ici (ni par la taille, ni par la date).
//
// localContent et remoteContent : empreintes de contenu (contentFingerprint) ; égales et non vides,
// les copies sont identiques même si leurs dates diffèrent. "" (inconnue, ancien agent) : seule
// l'empreinte compte.
func decideSync(localFP, localState string, localNewest int64, remoteFP, remoteState string, remoteNewest int64, localContent, remoteContent string) syncAction {
	switch {
	case localFP == remoteFP:
		return syncUpToDate
	case localContent != "" && localContent == remoteContent:
		return syncUpToDate
	case localFP == "":
		return syncReceive
	case remoteFP == "":
		return syncSend
	case localState == "" && remoteState == "":
		switch {
		case localNewest > remoteNewest:
			return syncSend
		case localNewest < remoteNewest:
			return syncReceive
		}
		return syncConflict
	}
	lc := localFP != localState
	rc := remoteFP != remoteState
	switch {
	case !lc && rc:
		return syncReceive
	case lc && !rc:
		return syncSend
	}
	return syncConflict
}

// String : libellé court affiché à l'utilisateur.
func (a syncAction) String() string {
	switch a {
	case syncUpToDate:
		return "à jour"
	case syncSend:
		return "envoi"
	case syncReceive:
		return "réception"
	case syncConflict:
		return "conflit : à départager"
	}
	return "?"
}

// syncUsersDir : users/ effectif d'un jeu sur cette machine. En overlay, ce sont les sauvegardes de
// la couche (savesDir/<jeu>) quand elles existent. En mode copy, la copie de travail
// (cacheDir/wine/<jeu>) si elle est marquée .wsquashfs_cache, sinon la mise de côté de
// restoreCopyUsers. Le mode en vigueur n'est pas enregistré : l'overlay l'emporte quand il a des
// sauvegardes, car il n'existe que lorsque le mode copy est le repli d'un overlay en échec.
// Le jeu doit être arrêté (la couche et la copie sont alors à jour).
func syncUsersDir(name string) string {
	overlay := filepath.Join(conf.savesDir, name, "drive_c", "users")
	if isDirPath(overlay) {
		return overlay
	}
	copyDir := filepath.Join(conf.cacheDir, "wine", name)
	if isFile(copyDir + "/.wsquashfs_cache") {
		return copyDir + "/drive_c/users"
	}
	if stash := copyUsersStash(name); isDirPath(stash) {
		return stash
	}
	return overlay
}

// syncGames : jeux de cette machine synchronisables, triés : ceux dont les sauvegardes ont une
// empreinte non vide, où qu'elles soient (couche de l'overlay, copie de travail cacheDir/wine, mise
// de côté du mode copy cacheDir/copy-users). Liste commune à --sync et à la route /v1/saves/list de
// l'agent : les deux côtés voient les mêmes jeux.
func syncGames() []string {
	candidates := savedGames()
	candidates = append(candidates, visibleDirs(filepath.Join(conf.cacheDir, "wine"))...)
	candidates = append(candidates, visibleDirs(filepath.Dir(copyUsersStash("x")))...)
	seen := map[string]bool{}
	var out []string
	for _, g := range candidates {
		if !validSyncGame(g) || seen[g] {
			continue
		}
		seen[g] = true
		users := syncUsersDir(g)
		if saveFingerprint(users, collectSaveFiles(users)) != "" {
			out = append(out, g)
		}
	}
	slices.Sort(out)
	return out
}
