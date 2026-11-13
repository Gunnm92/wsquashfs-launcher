package main

// Synchronisation manuelle des sauvegardes de partie entre machines (--sync) :
// le cœur local, sans réseau. Le périmètre est celui de --save (copyGameSaves) :
// sous users/, les profils hors comptes système, seulement les sous-dossiers
// saveProfileDirs, sans les caches. Les fonctions prennent des chemins explicites
// pour être testées en simulant deux machines dans un même test.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
	sort.Strings(out)
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
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
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
	sort.Strings(out)
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
	sort.Strings(lines)
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
			st, err := os.Lstat(filepath.Join(users, filepath.FromSlash(dirRel)))
			if err != nil {
				return "", err
			}
			hdr := &tar.Header{
				Typeflag: tar.TypeDir,
				Name:     archiveEntryName(dirRel, single) + "/",
				Mode:     int64(st.Mode().Perm()),
				ModTime:  time.Unix(st.ModTime().Unix(), 0),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return "", err
			}
		}
		fp := filepath.Join(users, filepath.FromSlash(f.Rel))
		st, err := os.Lstat(fp)
		if err != nil || !st.Mode().IsRegular() {
			return "", fmt.Errorf("%s : fichier absent ou non régulier", f.Rel)
		}
		src, err := os.Open(fp)
		if err != nil {
			return "", err
		}
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     archiveEntryName(f.Rel, single),
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
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return profile, nil
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

// applySaveArchive : remplace le périmètre de users/ par le contenu de l'archive. Tout est extrait
// d'abord dans un dossier temporaire frère : une archive refusée ne modifie rien. Seuls les fichiers du
// périmètre actuel sont retirés ; le reste de users/ n'est jamais touché. Le jeu doit être arrêté.
func applySaveArchive(r io.Reader, users string, origProfile string) error {
	parent := filepath.Dir(users)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".wsq-sync-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	local, err := localSyncProfile(users, origProfile)
	if err != nil {
		return err
	}
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()

	var moves []syncMove
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		e, err := checkSyncEntry(hdr)
		if err != nil {
			return err
		}
		profile := e.profile
		if profile == syncProfileToken {
			profile = local
		}
		rel := profile
		if e.rest != "" {
			rel += "/" + e.rest
		}
		p := filepath.Join(tmp, filepath.FromSlash(rel))
		if e.isDir {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, cerr := io.CopyN(f, tr, hdr.Size)
		if err := f.Close(); cerr == nil {
			cerr = err
		}
		if cerr != nil {
			return cerr
		}
		moves = append(moves, syncMove{
			src:   p,
			dst:   filepath.Join(users, filepath.FromSlash(rel)),
			mtime: hdr.ModTime.Unix(),
		})
	}

	// Tout a été extrait sans erreur, et rien n'est encore modifié : les destinations ne doivent pas
	// passer par un lien (MkdirAll et Rename suivraient le lien et écriraient hors de users/).
	if err := checkSyncDests(users, moves); err != nil {
		return err
	}
	// On remplace le périmètre.
	for _, f := range collectSaveFiles(users) {
		if err := os.Remove(filepath.Join(users, filepath.FromSlash(f.Rel))); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(users, 0o755); err != nil {
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

// syncAction : ce que la synchro doit faire pour un jeu.
type syncAction int

const (
	syncUpToDate syncAction = iota
	syncSend
	syncReceive
	syncConflictSend
	syncConflictReceive
)

// decideSync : sens de la synchro, d'après les empreintes locale et distante et l'état de la
// dernière synchro (chacune son empreinte). Si les deux côtés ont changé, le plus récent gagne.
func decideSync(localFP, localState string, localNewest int64, remoteFP, remoteState string, remoteNewest int64) syncAction {
	switch {
	case localFP == remoteFP:
		return syncUpToDate
	case localFP == "":
		return syncReceive
	case remoteFP == "":
		return syncSend
	}
	lc := localFP != localState
	rc := remoteFP != remoteState
	switch {
	case !lc && rc:
		return syncReceive
	case lc && !rc:
		return syncSend
	case localNewest >= remoteNewest:
		return syncConflictSend
	}
	return syncConflictReceive
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
	case syncConflictSend:
		return "conflit (envoi)"
	case syncConflictReceive:
		return "conflit (réception)"
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
	overlay := filepath.Join(savesDir, name, "drive_c", "users")
	if isDirPath(overlay) {
		return overlay
	}
	copyDir := filepath.Join(cacheDir, "wine", name)
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
	candidates = append(candidates, visibleDirs(filepath.Join(cacheDir, "wine"))...)
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
	sort.Strings(out)
	return out
}
