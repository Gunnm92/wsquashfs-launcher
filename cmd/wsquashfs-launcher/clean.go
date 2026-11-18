package main

// Commandes --clean (copies de travail, doublons, jeux sans image ni sauvegarde), --clear (ne garde
// que drive_c/users) et --erase (archive, puis efface les sauvegardes d'un jeu).

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ── --clean (01/10) ────────────────────────────────────────────────────

// underOpaque : vrai si rel est sous un dossier opaque (le fichier de
// l'upperdir est alors le seul visible).
func underOpaque(upper, rel string) bool {
	d := rel
	for strings.Contains(d, "/") {
		d = d[:strings.LastIndex(d, "/")]
		if _, ok := launch.opaqueCache[d]; !ok {
			launch.opaqueCache[d] = "n"
			if isFile(upper+"/"+d+"/.wh..wh..opq") || hasOpaqueXattr(upper+"/"+d) {
				launch.opaqueCache[d] = "y"
			}
		}
		if launch.opaqueCache[d] == "y" {
			return true
		}
	}
	return false
}

// hasOpaqueXattr : attribut opaque fuse-overlayfs ou noyau à "y".
func hasOpaqueXattr(p string) bool {
	out, err := exec.Command("getfattr", "-d", "-m", `^user\.(fuseoverlayfs|overlay)\.opaque$`, p).Output()
	return err == nil && strings.Contains(string(out), `="y"`)
}

// hasWineXattr : attributs Wine (DOS, sécurité) présents.
func hasWineXattr(p string) bool {
	out, err := exec.Command("getfattr", "-d", "-m", `^user\.(wine|DOSATTRIB)`, p).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// cleanResult : ce que --clean a fait des sauvegardes d'un jeu.
type cleanResult struct {
	skip  string // raison pour laquelle les doublons n'ont pas été cherchés
	n     int    // doublons retirés
	bytes int64  // place libérée
}

// cleanSaves : --clean pour un jeu. Ménage sans risque : ne retire des
// sauvegardes que les doublons certains (fichiers identiques à l'image, DLL
// que Proton recopie à chaque lancement). Tout ce que le jeu a écrit reste.
func cleanSaves(name string) cleanResult {
	saves := conf.savesDir + "/" + name
	lock, idle := lockIdleGame(name)
	if !idle {
		return cleanResult{skip: skipBusy}
	}
	if lock != nil {
		defer lock.Close()
	}
	skip, n, freed := removeDuplicates(name, saves)
	return cleanResult{skip, n, freed}
}

// Raisons de cleanResult.skip.
const (
	skipBusy    = "en cours"
	skipNoImage = "image inconnue"
)

// keepOnlyUsers : ne laisse dans une couche de sauvegardes que drive_c/users.
// Faux s'il ne reste rien (dossier supprimé).
func keepOnlyUsers(saves string) bool {
	entries, _ := os.ReadDir(saves)
	for _, e := range entries {
		if e.Name() != "drive_c" {
			_ = os.RemoveAll(saves + "/" + e.Name())
		}
	}
	if isDirPath(saves + "/drive_c") {
		if isDirPath(saves + "/drive_c/users") {
			_ = os.RemoveAll(saves + "/.users")
			if os.Rename(saves+"/drive_c/users", saves+"/.users") != nil {
				return true
			}
		}
		_ = os.RemoveAll(saves + "/drive_c")
		if isDirPath(saves + "/.users") {
			_ = os.MkdirAll(saves+"/drive_c", 0o755)
			_ = os.Rename(saves+"/.users", saves+"/drive_c/users")
		}
	}
	_ = os.Remove(saves)
	return isDirPath(saves)
}

// removeDuplicates : retire des sauvegardes les fichiers identiques à l'image
// du jeu (copiés sans être modifiés), puis les DLL que Proton recopie.
func removeDuplicates(name, saves string) (skip string, n int, bytesFreed int64) {
	img := ""
	if data, err := os.ReadFile(conf.savesDir + "/.images/" + name); err == nil {
		img = strings.TrimRight(string(data), "\n")
	}
	switch {
	case img == "" || !pathExists(img):
		skip = skipNoImage
	case !cmdOK("getfattr", "--version"):
		skip = "getfattr absent (paquet attr)"
	default:
		_ = os.MkdirAll(conf.cacheDir, 0o755)
		mnt, err := os.MkdirTemp(conf.cacheDir, "purge.")
		if err != nil {
			return
		}
		root := mnt
		isDirImg := false
		if st, err := os.Stat(img); err == nil && st.IsDir() {
			root = img
			isDirImg = true
		}
		if isDirImg || mountSquashfs(img, mnt) {
			if !isDirImg {
				// Interrompu (signal) : cleanup démonte l'image au lieu de la laisser montée.
				purgeMount.Store(&mnt)
			}
			launch.opaqueCache = map[string]string{}
			_ = filepath.Walk(saves, func(f string, info os.FileInfo, err error) error {
				if err != nil || !info.Mode().IsRegular() {
					return nil
				}
				rel := strings.TrimPrefix(f, saves+"/")
				if strings.HasPrefix(filepath.Base(rel), ".wh.") {
					return nil
				}
				lower := root + "/" + rel
				lst, err := os.Lstat(lower)
				if err != nil || !lst.Mode().IsRegular() {
					return nil
				}
				if info.Size() != lst.Size() || underOpaque(saves, rel) || hasWineXattr(f) {
					return nil
				}
				if !sameContent(f, lower) {
					return nil
				}
				if os.Remove(f) == nil {
					n++
					bytesFreed += info.Size()
				}
				return nil
			})
			if !isDirImg {
				umountRetry(mnt)
				purgeMount.Store(nil)
			}
		} else {
			skip = "montage de l'image impossible"
		}
		_ = os.Remove(mnt)
	}
	// Prefix Proton : DXVK, vkd3d-proton et nvapi recopiés à chaque lancement.
	if cfg, err := os.ReadFile(saves + "/config_info"); err == nil {
		lines := strings.Split(string(cfg), "\n")
		if len(lines) >= 3 {
			lib := strings.TrimRight(lines[2], "\r")
			if lib != "" && isDirPath(lib+"wine") {
				dirs := []string{saves + "/drive_c/windows/system32", saves + "/drive_c/windows/syswow64"}
				for _, d := range dirs {
					entries, _ := os.ReadDir(d)
					for _, e := range entries {
						if !e.Type().IsRegular() {
							continue
						}
						f := d + "/" + e.Name()
						for _, comp := range []string{"dxvk", "vkd3d-proton", "nvapi"} {
							for _, arch := range []string{"x86_64", "i386"} {
								c := lib + "wine/" + comp + "/" + arch + "-windows/" + e.Name()
								if isFile(c) && sameContent(f, c) {
									if info, err := os.Stat(f); err == nil && os.Remove(f) == nil {
										n++
										bytesFreed += info.Size()
									}
									goto next
								}
							}
						}
					next:
					}
				}
			}
		}
	}
	return skip, n, bytesFreed
}

// sameContent : deux fichiers identiques octet par octet (cmp -s).
func sameContent(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	for {
		ba := make([]byte, 64*1024)
		bb := make([]byte, 64*1024)
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if ea == io.EOF || ea == io.ErrUnexpectedEOF {
			return eb == io.EOF || eb == io.ErrUnexpectedEOF
		}
		if ea != nil || eb != nil {
			return false
		}
	}
}

// cleanCache : copies de travail (mnt, wine, work) des jeux qui ne tournent pas.
// Retourne la place libérée (fichiers des copies supprimées).
func cleanCache() (freed int64) {
	unmountAllStale()
	dirs, _ := filepath.Glob(conf.cacheDir + "/mnt/*")
	more, _ := filepath.Glob(conf.cacheDir + "/wine/*")
	dirs = append(dirs, more...)
	more, _ = filepath.Glob(conf.savesDir + "/.work/*")
	dirs = append(dirs, more...)
	more, _ = filepath.Glob(conf.cacheDir + "/work/*")
	dirs = append(dirs, more...)
	for _, d := range dirs {
		if _, err := os.Lstat(d); err != nil {
			continue
		}
		g := filepath.Base(d)
		lock, idle := lockIdleGame(g)
		if !idle || isMountPoint(conf.cacheDir+"/mnt/"+g) {
			if lock != nil {
				lock.Close()
			}
			continue
		}
		// Mode copy : les sauvegardes de partie sont dans la copie.
		if filepath.Dir(d) != conf.cacheDir+"/wine" || stashCopyUsers(d, g) {
			freed += filesSize(d)
			_ = os.RemoveAll(d)
		} else {
			warn(g + " : sauvegardes de la copie de travail impossibles à mettre de côté, copie gardée")
		}
		if lock != nil {
			lock.Close()
		}
	}
	freed += pruneSyncTemp()
	uiStatusLine(stOK, "Copies de travail supprimées")
	return freed
}

// pruneOldProtons : --clean (18/11) — anciennes versions de GE-Proton et
// UMU-Proton installées par --setup (~/.local/share/Steam/compatibilitytools.d),
// la plus récente de chaque famille gardée. Un Proton en cours d'utilisation
// (chemin dans la ligne de commande ou l'environnement d'un processus) est
// gardé. Les prefixes créés par une version retirée passent à la plus récente
// de la famille (findProtonRunner). Proton du système, de Steam ou de Heroic :
// jamais touchés.
func pruneOldProtons() (removed []string, freed int64) {
	dir := filepath.Join(os.Getenv("HOME"), ".local/share/Steam/compatibilitytools.d")
	entries, _ := filepath.Glob(dir + "/*")
	newest := map[string]string{}
	var candidates []string
	for _, d := range entries {
		if st, err := os.Lstat(d); err != nil || !st.IsDir() || !isExecutable(d+"/proton") {
			continue
		}
		fam := protonFamilyOf(filepath.Base(d))
		if fam != "GE-Proton" && fam != "UMU-Proton" {
			continue
		}
		candidates = append(candidates, d)
		if cur := newest[fam]; cur == "" || versionLess(filepath.Base(cur), filepath.Base(d)) {
			newest[fam] = d
		}
	}
	for _, d := range candidates {
		if newest[protonFamilyOf(filepath.Base(d))] == d || protonInUse(d) {
			continue
		}
		size := filesSize(d)
		if os.RemoveAll(d) == nil {
			removed = append(removed, filepath.Base(d))
			freed += size
		}
	}
	return removed, freed
}

// protonInUse : un processus utilise ce Proton (chemin dans sa ligne de
// commande ou son environnement, PROTONPATH compris).
func protonInUse(d string) bool {
	needle := []byte(d + "/")
	procs, _ := filepath.Glob("/proc/[0-9]*")
	for _, p := range procs {
		for _, f := range []string{"/cmdline", "/environ"} {
			data, err := os.ReadFile(p + f)
			if err == nil && (bytes.Contains(data, needle) || bytes.Contains(data, []byte(d+"\x00"))) {
				return true
			}
		}
	}
	return false
}

// filesSize : taille des fichiers ordinaires d'un dossier.
func filesSize(dir string) (total int64) {
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// cmdClean : --clean. Ménage sans risque : copies de travail, doublons
// certains des sauvegardes, archives les plus anciennes de --erase. Les jeux
// sont regroupés par rubrique : doublons retirés (tableau), puis bilan.
func cmdClean() {
	uiTitle("Nettoyage", "copies de travail et doublons des sauvegardes")
	cacheFreed := cleanCache()
	archives, archivesFreed := pruneBackups()
	orphans := pruneOrphanSaves()
	protons, protonsFreed := pruneOldProtons()

	games := savedGames()
	var busy, noImage, failed []string
	var rows [][]string
	cleaned, untouched, files := 0, 0, 0
	var freed int64
	for _, g := range games {
		switch r := cleanSaves(g); {
		case r.skip == skipBusy:
			busy = append(busy, g)
		case r.skip == skipNoImage:
			noImage = append(noImage, g)
		case r.skip != "" && r.n == 0:
			failed = append(failed, g+" : "+r.skip)
		case r.n == 0:
			untouched++
		default:
			rows = append(rows, []string{truncate(g, uiNameMax), strconv.Itoa(r.n), sizeText(r.bytes)})
			cleaned++
			files += r.n
			freed += r.bytes
		}
	}
	if cleaned > 0 {
		uiSection("Doublons retirés")
		var total uiCell
		if cleaned > 1 {
			rows = append(rows, []string{"Total", strconv.Itoa(files), sizeText(freed)})
			total = uiBoldRow(len(rows) - 1)
		}
		uiTable([]string{"Jeu", "Fichiers", "Libéré"}, rows, []int{1, 2}, total)
	}
	uiList("En cours ou montés, ignorés", busy)
	uiList("Image inconnue, doublons non cherchés (lancer le jeu une fois)", noImage)
	uiList("Doublons non cherchés", failed)
	uiList("Sans image ni sauvegarde, retirés", orphans)
	uiList("Anciennes versions de Proton retirées (la plus récente est gardée)", protons)

	uiSection("Bilan")
	placeLibre := cacheFreed + archivesFreed + freed + protonsFreed
	bilan := []kv{{"Copies de travail", sizeText(cacheFreed) + " libérés"}}
	if archives > 0 {
		bilan = append(bilan, kv{"Archives", plural(archives, "supprimée", "supprimées") + ", " + sizeText(archivesFreed) + " libérés"})
	}
	if cleaned > 0 {
		bilan = append(bilan, kv{"Doublons", plural(files, "fichier", "fichiers") + " dans " + plural(cleaned, "jeu", "jeux") + ", " + sizeText(freed) + " libérés"})
	} else {
		bilan = append(bilan, kv{"Doublons", "aucun à retirer"})
	}
	if untouched > 0 {
		bilan = append(bilan, kv{"Sans doublon", plural(untouched, "jeu", "jeux")})
	}
	if len(orphans) > 0 {
		bilan = append(bilan, kv{"Sans image ni sauvegarde", plural(len(orphans), "jeu retiré", "jeux retirés")})
	}
	if len(protons) > 0 {
		bilan = append(bilan, kv{"Proton", plural(len(protons), "ancienne version retirée", "anciennes versions retirées") + ", " + sizeText(protonsFreed) + " libérés"})
	}

	if skipped := len(busy) + len(noImage) + len(failed); skipped > 0 {
		bilan = append(bilan, kv{"Non traités", plural(skipped, "jeu", "jeux") + " (voir ci-dessus)"})
	}
	bilan = append(bilan,
		kv{"Place libérée", sizeText(placeLibre)},
		kv{"Sauvegardes", conf.savesDir + " (" + duHuman(conf.savesDir) + " au total)"})
	uiKV(2, bilan...)
	uiVerdict(true, "Nettoyage terminé : "+sizeText(placeLibre)+" libérés")
}

// ── --clear (01/10) et --erase (12/11) ─────────────────────────────────

// clearSaves : --clear pour un jeu. Ne garde que drive_c/users (les
// sauvegardes de partie) : prefix, registre et fichiers écrits ailleurs sont
// retirés et recréés au lancement suivant.
func clearSaves(name string, g uiGames) {
	saves := conf.savesDir + "/" + name
	lock, idle := lockIdleGame(name)
	if !idle {
		g.line(stSkip, name, "monté ou en cours, ignoré")
		return
	}
	if lock != nil {
		defer lock.Close()
	}
	if keepOnlyUsers(saves) {
		g.line(stOK, name, "gardé "+duHuman(saves)+" (drive_c/users)")
	} else {
		g.line(stOK, name, "rien à garder, supprimé")
	}
}

// eraseSaves : --erase pour un jeu. Archive drive_c/users, puis supprime
// toute la couche de sauvegardes : le jeu repart de son image. Rien n'est
// supprimé si l'archive échoue.
func eraseSaves(name string, g uiGames) {
	saves := conf.savesDir + "/" + name
	lock, idle := lockIdleGame(name)
	if !idle {
		g.line(stSkip, name, "monté ou en cours, ignoré")
		return
	}
	if lock != nil {
		defer lock.Close()
	}
	// Mode copy : cleanCache vient de mettre drive_c/users de côté.
	stash := copyUsersStash(name)
	parent, member := "", ""
	switch {
	case isDirPath(saves + "/drive_c/users"):
		parent, member = saves, "drive_c/users"
	case isDirPath(stash):
		parent, member = filepath.Dir(stash), name
	}
	archive := ""
	if parent != "" {
		var err error
		if archive, err = archiveUsers(name, parent, member); err != nil {
			g.line(stFail, name, "archive impossible ("+err.Error()+"), sauvegardes gardées")
			return
		}
	}
	forgetGameSaves(name)
	if archive != "" {
		g.line(stOK, name, "supprimé, archive : "+filepath.Base(archive)+" ("+duHuman(archive)+")")
	} else {
		g.line(stOK, name, "supprimé (pas de drive_c/users à archiver)")
	}
}

// forgetGameSaves : --erase : efface tout l'overlay d'un jeu (couche de sauvegardes, mise de côté du
// mode copy) et son chemin d'image mémorisé : il repart de son image et --list ne le montre plus.
// Le jeu doit être arrêté (verrou tenu par l'appelant).
func forgetGameSaves(name string) {
	_ = os.RemoveAll(filepath.Join(conf.savesDir, name))
	_ = os.RemoveAll(copyUsersStash(name))
	_ = os.Remove(filepath.Join(conf.savesDir, ".images", name))
}

// saveTargetsOf : jeux visés par --clear ou --erase (un seul, ou tous).
// ok faux si le jeu nommé n'a pas de sauvegarde.
func saveTargetsOf(args []string) ([]string, bool) {
	if len(args) == 0 {
		return savedGames(), true
	}
	name := gameNameOf(args[0])
	if !isDirPath(conf.savesDir+"/"+name) && !isDirPath(copyUsersStash(name)) {
		uiStatusLine(stSkip, "Aucune sauvegarde pour "+name)
		return nil, false
	}
	return []string{name}, true
}

// cmdClear : --clear [jeu].
func cmdClear(args []string) int {
	targets, ok := saveTargetsOf(args)
	if !ok {
		return 0
	}
	uiTitle("Sauvegardes réduites à drive_c/users", plural(len(targets), "jeu", "jeux"))
	uiNote("Scores et réglages enregistrés ailleurs (jeux arcade, dossier du jeu,",
		"registre) seront perdus. Aucune archive n'est faite (tout effacer, avec",
		"archive : --erase).")
	if isTerminal() && !confirm("Continuer ?") {
		uiStatusLine(stSkip, "Abandon : rien n'a été modifié")
		return 1
	}
	uiOut("")
	cleanCache()
	g := newUIGames(targets)
	for _, name := range targets {
		clearSaves(name, g)
	}
	return 0
}

// cmdErase : --erase [jeu].
func cmdErase(args []string) int {
	targets, ok := saveTargetsOf(args)
	if !ok {
		return 0
	}
	uiTitle("Effacement des sauvegardes", plural(len(targets), "jeu", "jeux"))
	uiNote("Chaque jeu repart de son image. drive_c/users est archivé avant dans", backupDir())
	uiNote("Restauration :")
	uiCode("tar -xf <archive> -C " + conf.savesDir + "/<jeu>/")
	if isTerminal() && !confirm("Continuer ?") {
		uiStatusLine(stSkip, "Abandon : rien n'a été modifié")
		return 1
	}
	uiOut("")
	cleanCache()
	g := newUIGames(targets)
	for _, name := range targets {
		eraseSaves(name, g)
	}
	return 0
}

// pruneOrphanSaves : --clean : jeux sans image (renommée ou retirée, introuvable aussi dans les roms/)
// qui n'ont plus de sauvegarde (gameHasSaves faux) : il ne reste que leur overlay (prefix, fichiers
// temporaires), inutile sans image ; leur dossier et leur chemin mémorisé sont retirés. Jeux en cours
// ou montés : ignorés. Retourne les jeux retirés.
func pruneOrphanSaves() []string {
	var out []string
	for _, g := range knownGames() {
		if img := gameImage(g); (img != "" && pathExists(img)) || gameHasSaves(g) {
			continue
		}
		lock, ok := lockIdleGame(g)
		if !ok {
			continue
		}
		if os.RemoveAll(filepath.Join(conf.savesDir, g)) == nil {
			_ = os.Remove(filepath.Join(conf.savesDir, ".images", g))
			out = append(out, g)
		}
		if lock != nil {
			lock.Close()
		}
	}
	return out
}
