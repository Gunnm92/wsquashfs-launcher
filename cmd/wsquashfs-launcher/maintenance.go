package main

// Commandes de maintenance (script bash, section 8) : --clean, --clear,
// --erase, --umount, --list, --doctor, --info, --show, --pack, --install, --save,
// --saves,
// --agent, --pair, --update.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// maintenanceMain : dispatch des commandes de maintenance.
func maintenanceMain() int {
	switch action {
	case "clean":
		cmdClean()
	case "clear":
		return cmdClear(cmdArgs)
	case "erase":
		return cmdErase(cmdArgs)
	case "umount":
		if len(cmdArgs) > 0 {
			umountGame(gameNameOf(cmdArgs[0]))
			return 0
		}
		entries, _ := os.ReadDir(cacheDir + "/wine")
		for _, e := range entries {
			if e.IsDir() {
				umountGame(e.Name())
			}
		}
		unmountAllStale()
	case "pack":
		return packPrefix(cmdArgs)
	case "install":
		return installGame(cmdArgs)
	case "info":
		return showInfo(cmdArgs)
	case "show":
		return cmdShow(cmdArgs)
	case "save":
		return cmdSave(cmdArgs)
	case "saves":
		return cmdSaves()
	case "list":
		cmdList()
	case "doctor":
		return cmdDoctorMain(cmdArgs)
	case "agent":
		return cmdAgentMain(cmdArgs)
	case "pair":
		return cmdPair(cmdArgs)
	case "sync":
		return cmdSync(cmdArgs)
	case "trial":
		return cmdTrialMain(cmdArgs)
	case "update":
		return cmdUpdate()
	}
	return 0
}

// duHuman : taille lisible (du -sh) ; vide si erreur.
func duHuman(p string) string {
	// du sort une taille même en erreur (sous-dossier illisible) : code ignoré.
	out, _ := exec.Command("du", "-sh", p).Output()
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// humanBytes : numfmt --to=iec --suffix=o.
func humanBytes(n int64) string {
	out, err := exec.Command("numfmt", "--to=iec", "--suffix=o", strconv.FormatInt(n, 10)).Output()
	if err != nil {
		return strconv.FormatInt(n, 10) + "o"
	}
	return strings.TrimSpace(string(out))
}

// ── --clean (01/10) ────────────────────────────────────────────────────

// underOpaque : vrai si rel est sous un dossier opaque (le fichier de
// l'upperdir est alors le seul visible).
func underOpaque(upper, rel string) bool {
	d := rel
	for strings.Contains(d, "/") {
		d = d[:strings.LastIndex(d, "/")]
		if _, ok := opaqueCache[d]; !ok {
			opaqueCache[d] = "n"
			if isFile(upper+"/"+d+"/.wh..wh..opq") || hasOpaqueXattr(upper+"/"+d) {
				opaqueCache[d] = "y"
			}
		}
		if opaqueCache[d] == "y" {
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
	saves := savesDir + "/" + name
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
	if data, err := os.ReadFile(savesDir + "/.images/" + name); err == nil {
		img = strings.TrimRight(string(data), "\n")
	}
	switch {
	case img == "" || !pathExists(img):
		skip = skipNoImage
	case !cmdOK("getfattr", "--version"):
		skip = "getfattr absent (paquet attr)"
	default:
		_ = os.MkdirAll(cacheDir, 0o755)
		mnt, err := os.MkdirTemp(cacheDir, "purge.")
		if err != nil {
			return
		}
		root := mnt
		isDirImg := false
		if st, err := os.Stat(img); err == nil && st.IsDir() {
			root = img
			isDirImg = true
		}
		if isDirImg || execQuiet("squashfuse", img, mnt) == nil {
			opaqueCache = map[string]string{}
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

// isDirPath : chemin existant et dossier.
func isDirPath(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
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

// backupDir : archives de --erase.
func backupDir() string {
	return envOr("WSQUASHFS_BACKUP_DIR", filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/backups"))
}

// backupsKept : archives gardées par jeu ; --clean retire les plus anciennes.
const backupsKept = 3

var backupNameRe = regexp.MustCompile(`^(.*)-(\d{8}-\d{6})(-\d+)?\.tar\.(zst|gz)$`)

// pruneBackups : ne garde que les backupsKept dernières archives de chaque
// jeu, et retire les archives inachevées (.part). Les autres fichiers du
// dossier ne sont pas touchés.
func pruneBackups() (n int, size int64) {
	dir := backupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	byGame := map[string][]string{}
	var doomed []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		switch m := backupNameRe.FindStringSubmatch(e.Name()); {
		case m != nil:
			byGame[m[1]] = append(byGame[m[1]], e.Name())
		case strings.HasSuffix(e.Name(), ".part"):
			doomed = append(doomed, e.Name())
		}
	}
	for _, names := range byGame {
		// L'horodatage du nom donne l'ordre ; à date égale, le suffixe -N.
		sort.Slice(names, func(i, j int) bool {
			a, b := backupNameRe.FindStringSubmatch(names[i]), backupNameRe.FindStringSubmatch(names[j])
			if a[2] != b[2] {
				return a[2] > b[2]
			}
			return len(a[3]) > len(b[3]) || (len(a[3]) == len(b[3]) && a[3] > b[3])
		})
		if len(names) > backupsKept {
			doomed = append(doomed, names[backupsKept:]...)
		}
	}
	sort.Strings(doomed)
	for _, name := range doomed {
		f := filepath.Join(dir, name)
		if st, err := os.Stat(f); err == nil && os.Remove(f) == nil {
			n++
			size += st.Size()
		}
	}
	if n > 0 {
		uiStatusLine(stOK, fmt.Sprintf("Anciennes archives supprimées : %d (%s)", n, dir))
	}
	return n, size
}

// archiveUsers : archive <parent>/<member> dans backupDir, sous le nom
// <jeu>-AAAAMMJJ-HHMMSS.tar.zst (tar.gz sans zstd). L'archive n'apparaît
// sous son nom qu'une fois complète.
func archiveUsers(name, parent, member string) (string, error) {
	dir := backupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext, flag := "tar.zst", "--zstd"
	if _, err := exec.LookPath("zstd"); err != nil {
		ext, flag = "tar.gz", "-z"
	}
	base := filepath.Join(dir, name+"-"+time.Now().Format("20060102-150405"))
	out := base + "." + ext
	for i := 2; pathExists(out); i++ {
		out = fmt.Sprintf("%s-%d.%s", base, i, ext)
	}
	part := out + ".part"
	if err := execQuiet("tar", flag, "-cf", part, "-C", parent, member); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, out); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	chownToTarget(dir)
	return out, nil
}

// savedGames : jeux qui ont une couche de sauvegardes.
func savedGames() []string {
	return visibleDirs(savesDir)
}

// visibleDirs : sous-dossiers non cachés de d (glob */ : les noms en point sont
// ignorés, comme .work et .images).
func visibleDirs(d string) []string {
	entries, _ := os.ReadDir(d)
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// cleanCache : copies de travail (mnt, wine, work) des jeux qui ne tournent pas.
// Retourne la place libérée (fichiers des copies supprimées).
func cleanCache() (freed int64) {
	unmountAllStale()
	dirs, _ := filepath.Glob(cacheDir + "/mnt/*")
	more, _ := filepath.Glob(cacheDir + "/wine/*")
	dirs = append(dirs, more...)
	more, _ = filepath.Glob(savesDir + "/.work/*")
	dirs = append(dirs, more...)
	more, _ = filepath.Glob(cacheDir + "/work/*")
	dirs = append(dirs, more...)
	for _, d := range dirs {
		if _, err := os.Lstat(d); err != nil {
			continue
		}
		g := filepath.Base(d)
		lock, idle := lockIdleGame(g)
		if !idle || isMountPoint(cacheDir+"/mnt/"+g) {
			if lock != nil {
				lock.Close()
			}
			continue
		}
		// Mode copy : les sauvegardes de partie sont dans la copie.
		if filepath.Dir(d) != cacheDir+"/wine" || stashCopyUsers(d, g) {
			freed += filesSize(d)
			_ = os.RemoveAll(d)
		} else {
			warn(g + " : sauvegardes de la copie de travail impossibles à mettre de côté, copie gardée")
		}
		if lock != nil {
			lock.Close()
		}
	}
	uiStatusLine(stOK, "Copies de travail supprimées")
	return freed
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

	uiSection("Bilan")
	placeLibre := cacheFreed + archivesFreed + freed
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
	if skipped := len(busy) + len(noImage) + len(failed); skipped > 0 {
		bilan = append(bilan, kv{"Non traités", plural(skipped, "jeu", "jeux") + " (voir ci-dessus)"})
	}
	bilan = append(bilan,
		kv{"Place libérée", sizeText(placeLibre)},
		kv{"Sauvegardes", savesDir + " (" + duHuman(savesDir) + " au total)"})
	uiKV(2, bilan...)
	uiVerdict(true, "Nettoyage terminé : "+sizeText(placeLibre)+" libérés")
}

// ── --clear (01/10) et --erase (12/11) ─────────────────────────────────

// clearSaves : --clear pour un jeu. Ne garde que drive_c/users (les
// sauvegardes de partie) : prefix, registre et fichiers écrits ailleurs sont
// retirés et recréés au lancement suivant.
func clearSaves(name string, g uiGames) {
	saves := savesDir + "/" + name
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
	saves := savesDir + "/" + name
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
	_ = os.RemoveAll(saves)
	_ = os.RemoveAll(stash)
	if archive != "" {
		g.line(stOK, name, "supprimé, archive : "+filepath.Base(archive)+" ("+duHuman(archive)+")")
	} else {
		g.line(stOK, name, "supprimé (pas de drive_c/users à archiver)")
	}
}

// saveTargetsOf : jeux visés par --clear ou --erase (un seul, ou tous).
// ok faux si le jeu nommé n'a pas de sauvegarde.
func saveTargetsOf(args []string) ([]string, bool) {
	if len(args) == 0 {
		return savedGames(), true
	}
	name := gameNameOf(args[0])
	if !isDirPath(savesDir+"/"+name) && !isDirPath(copyUsersStash(name)) {
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
	uiCode("tar -xf <archive> -C " + savesDir + "/<jeu>/")
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

// isTerminal : entrée standard sur un terminal (test -t 0 : ioctl TCGETS, car
// /dev/null est aussi un périphérique caractère).
func isTerminal() bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

// ── --list (13/10) ─────────────────────────────────────────────────────

// knownGames : jeux avec sauvegardes, copie de travail, ou image enregistrée.
func knownGames() []string {
	set := map[string]bool{}
	for _, g := range savedGames() {
		set[g] = true
	}
	for _, g := range visibleDirs(cacheDir + "/wine") {
		set[g] = true
	}
	imgs, _ := os.ReadDir(savesDir + "/.images")
	for _, e := range imgs {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			set[e.Name()] = true
		}
	}
	var out []string
	for g := range set {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// gameState : état d'un jeu (en cours, verrouillé, monté, -).
func gameState(name string) string {
	switch {
	case gameInUse(name):
		return "en cours"
	case gameLocked(name):
		return "verrouillé"
	case isMountPoint(cacheDir + "/wine/" + name):
		return "monté"
	}
	return "-"
}

// cmdList : --list.
func cmdList() {
	games := knownGames()
	if len(games) == 0 {
		uiStatusLine(stSkip, "Aucun jeu connu (aucun lancement enregistré dans "+savesDir+")")
		return
	}
	var rows [][]string
	for _, g := range games {
		version, size, image := "-", "aucune", ""
		img := ""
		if data, err := os.ReadFile(savesDir + "/.images/" + g); err == nil {
			img = strings.TrimRight(string(data), "\n")
		}
		switch {
		case img == "":
			image = "inconnue"
		case pathExists(img):
			image = img
			version = gameVersion(img)
			if version == "" {
				version = "-"
			}
		default:
			image = "introuvable : " + img
		}
		if isDirPath(savesDir + "/" + g) {
			size = duHuman(savesDir + "/" + g)
		}
		rows = append(rows, []string{truncate(g, uiNameMax), gameState(g), version, size, image})
	}
	// Taille totale : sur la ligne « Sauvegardes » en fin de liste.
	uiTitle("Jeux connus", plural(len(games), "jeu", "jeux"))
	uiTable([]string{"Jeu", "État", "Version", "Sauvegardes", "Image"}, rows, []int{3}, uiStateCell(1, 4))
	uiOut("")
	uiKVLine("Sauvegardes", savesDir+" ("+duHuman(savesDir)+" au total)")
}

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
		usageError(os.Args[0] + " --info <jeu.wsquashfs | dossier du jeu>")
	}
	src, err := filepath.Abs(args[0])
	if err != nil || !pathExists(src) {
		usageError(os.Args[0] + " --info <jeu.wsquashfs | dossier du jeu>")
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
	uiTitle(gameNameOf(src))
	if version == "" {
		version = "non renseignée (GAME_VERSION= dans l'autorun.cmd)"
	}
	pairs := []kv{{"Version", version}, {"Type", typ}}
	if cmdLine, ok := overrideVar(strings.TrimSuffix(src, "/")+".autorun", "CMD"); ok {
		pairs = append(pairs, kv{"Commande", cmdLine})
	} else if autorun != "" {
		pairs = append(pairs, kv{"Commande", keyLine(autorun, "CMD")})
	} else {
		pairs = append(pairs, kv{"Commande", "aucun autorun.cmd"})
	}
	pairs = append(pairs, kv{"Runner", infoRunner(autorun, joined)})
	if isFile(strings.TrimSuffix(src, "/") + ".autorun") {
		override := strings.TrimSuffix(src, "/") + ".autorun"
		pairs = append(pairs, kv{"Surcharge", filepath.Base(override) + " (" + overrideKeys(override) + ") — non lue par Batocera"})
	}
	pairs = append(pairs, kv{"Taille", duHuman(src)})
	uiKV(0, pairs...)
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
	sort.Strings(names)
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
	sort.Strings(names)
	return strings.Join(names, "\n")
}

// cmdShow : --show <jeu> [--set CLÉ=valeur]... (réglages finaux et couches).
func cmdShow(args []string) int {
	src := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--set" {
			if v, err := parseSet(argAt(args, i+1)); err == nil {
				setArgs = append(setArgs, v)
			} else {
				die(err.Error())
			}
			i++
			continue
		}
		if src != "" {
			usageError(os.Args[0] + " --show <jeu.wsquashfs | dossier> [--set CLÉ=valeur]...")
		}
		src = args[i]
	}
	if src == "" || !pathExists(src) {
		usageError(os.Args[0] + " --show <jeu.wsquashfs | dossier> [--set CLÉ=valeur]...")
	}
	if real, err := filepath.Abs(src); err == nil {
		src = real
	}
	wsquashfsFile = src
	gameName = gameNameOf(src)
	autorunOverride = strings.TrimSuffix(src, "/") + ".autorun"
	if !isFile(autorunOverride) {
		autorunOverride = ""
	}
	loadLayers(imageAutorun(src))

	uiTitle(gameName)
	machine := machineLayer
	if machine == "" {
		machine = machineLayerFile() + " (absent)"
	}
	folder := folderLayer
	if folder == "" {
		folder = filepath.Dir(src) + "/_defaults.autorun (absent)"
	}
	surcharge := strings.TrimSuffix(src, "/") + ".autorun"
	if autorunOverride == "" {
		surcharge += " (absent)"
	}
	couches := []kv{{"machine", machine}, {"dossier", folder}, {"image", "autorun.cmd"}, {"surcharge", surcharge}}
	if len(setArgs) > 0 {
		couches = append(couches, kv{"--set", strings.Join(setArgs, " ")})
	}
	uiSection("Couches (de la plus faible à la plus forte)")
	uiKV(2, couches...)

	shown := map[string]bool{}
	for _, k := range showKeys {
		shown[k] = true
	}
	var extra []string
	for k := range machineVal {
		if !shown[k] {
			extra = append(extra, k)
		}
	}
	for k := range layerVal {
		if !shown[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	extra = uniqueSorted(extra)
	var reglages [][]string
	for _, key := range append(append([]string{}, showKeys...), extra...) {
		layerLookup(key, "")
		if lookupVal == "" && lookupSrc == "défaut" {
			continue
		}
		reglages = append(reglages, []string{key, lookupVal, lookupSrc})
	}
	uiSection("Réglages")
	uiTable([]string{"Réglage", "Valeur", "Couche"}, reglages, nil, nil)

	if envLayers > 0 {
		uiSection("ENV")
		if mergedEnv() == "" {
			uiNote("aucune variable : ENV= vide")
		}
		var env [][]string
		seen := map[string]bool{}
		for _, n := range envOrder {
			if _, ok := envVal[n]; !ok || seen[n] {
				continue
			}
			seen[n] = true
			env = append(env, []string{envVal[n], envSrc[n]})
		}
		if len(env) > 0 {
			uiTable([]string{"Variable", "Couche"}, env, nil, nil)
		}
	}

	layerLookup("SAVEDIR", "")
	key := lookupVal
	layerLookup("SAVEFILES", "")
	sysdir, ok := batoceraSavesDir()
	overlay := kv{"Overlay", savesDir + "/" + gameName}
	sauvegardes := kv{"Sauvegardes", "dans l'overlay ; --save <jeu> les copie dans " + sysdir}
	switch {
	case !ok:
		sauvegardes.v = "hors d'un dossier roms/<système>/ (WSQUASHFS_BATOCERA_SAVES)"
	case key+lookupVal != "":
		sauvegardes.v = sysdir + " (SAVEDIR/SAVEFILES)"
	}
	uiSection("Sauvegardes")
	uiKV(2, overlay, sauvegardes)
	return 0
}

// showKeys : clés affichées par --show, dans cet ordre.
var showKeys = []string{"CMD", "DIR", "GAME_VERSION", "PROTON", "WINE", "RUNNER", "ARCH", "DXVK", "VKD3D", "D7VK",
	"NVAPI", "VKREFLEX", "FSR", "FSR4", "ESYNC", "FSYNC", "HIDRAW", "LANG", "VIRTUAL_DESKTOP", "SAVEDIR",
	"SAVEFILES", "PRE", "ATTACH", "ATTACH_PROCESS"}

// uniqueSorted : éléments distincts d'une liste triée.
func uniqueSorted(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// ── --pack / --install ────────────────────────────────────────────────

// packPrefix : prefix Wine ou jeu seul (dossier) → .wsquashfs (mksquashfs zstd).
func packPrefix(args []string) int {
	if len(args) == 0 || !isDirPath(args[0]) {
		usageError(os.Args[0] + " --pack <dossier.wine | dossier.pc> [sortie.wsquashfs]")
	}
	src := strings.TrimSuffix(args[0], "/")
	out := ""
	if len(args) > 1 {
		out = args[1]
	}
	if _, err := exec.LookPath("mksquashfs"); err != nil {
		die("mksquashfs absent (squashfs-tools)")
	}
	if out == "" {
		base := filepath.Base(src)
		switch lower := strings.ToLower(base); {
		case strings.HasSuffix(lower, ".wine"):
			base = base[:len(base)-5]
		case strings.HasSuffix(lower, ".pc"):
			base = base[:len(base)-3]
		}
		out = filepath.Dir(src) + "/" + base + ".wsquashfs"
	}
	saves := savesDir + "/" + strings.TrimSuffix(filepath.Base(out), ".wsquashfs")
	resetSaves := false
	if pathExists(out) {
		if !isTerminal() {
			die(out + " existe déjà (jamais écrasé sans confirmation au terminal)")
		}
		st, _ := os.Stat(out)
		if !confirm(fmt.Sprintf("%s existe déjà (%s, %s). L'écraser ?", out, duHuman(out), st.ModTime().Format("02/01 15:04"))) {
			uiStatusLine(stSkip, "Abandon : rien n'a été modifié")
			return 1
		}
		if isDirPath(saves) && confirm("Réinitialiser aussi ses sauvegardes ("+saves+") ?") {
			resetSaves = true
		}
	}
	if !isFile(src + "/autorun.cmd") {
		warn("pas d'autorun.cmd dans " + src + " (lançable seulement via --exec)")
	}
	tmp := out + ".part"
	_ = os.Remove(tmp)
	uiStatusLine(stInfo, "Création de "+out+" depuis "+src+"...")
	if err := runCmd("mksquashfs", src, tmp, "-comp", "zstd", "-noappend", "-quiet"); err != nil {
		_ = os.Remove(tmp)
		die("échec de mksquashfs, " + out + " inchangé")
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		die("remplacement de " + out + " impossible : " + err.Error())
	}
	if resetSaves {
		if os.RemoveAll(saves) == nil {
			uiStatusLine(stOK, "Sauvegardes réinitialisées : "+saves)
		}
	}
	uiStatusLine(stOK, out+" ("+duHuman(out)+")")
	if v := gameVersion(src); v != "" {
		uiKV(2, kv{"Version", v})
	} else if isFile(src + "/autorun.cmd") {
		uiKV(2, kv{"Version", "non renseignée (GAME_VERSION=... dans l'autorun.cmd, voir --info)"})
	}
	return 0
}

// installGame : --install <jeu.wsquashfs> [dossier] : extraction dans
// <dossier>/<Jeu>, pour jouer sans FUSE.
func installGame(args []string) int {
	if len(args) == 0 || !isFile(args[0]) || filepath.Ext(args[0]) != ".wsquashfs" {
		usageError(os.Args[0] + " --install <jeu.wsquashfs> [dossier de destination]")
	}
	img := args[0]
	if _, err := exec.LookPath("unsquashfs"); err != nil {
		die("unsquashfs absent (squashfs-tools)")
	}
	name := strings.TrimSuffix(filepath.Base(img), ".wsquashfs")
	parent := filepath.Dir(mustAbs(img))
	if len(args) > 1 {
		parent = args[1]
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		die("impossible de créer " + parent)
	}
	dest := strings.TrimSuffix(parent, "/") + "/" + name
	if pathExists(dest) {
		die(dest + " existe déjà (jamais écrasé)")
	}
	tmp := dest + ".part"
	_ = os.RemoveAll(tmp)
	uiStatusLine(stInfo, "Installation de "+name+" dans "+dest+"...")
	if err := runCmd("unsquashfs", "-no-xattrs", "-d", tmp, img); err != nil {
		_ = os.RemoveAll(tmp)
		die("échec de l'extraction, rien n'a été installé")
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.RemoveAll(tmp)
		die("installation dans " + dest + " impossible : " + err.Error())
	}
	if dropsToTargetUser() {
		_ = runCmd("chown", "-R", fmt.Sprintf("%d:%d", wineUID, wineGID), dest)
	}
	uiStatusLine(stOK, dest+" ("+duHuman(dest)+")")
	pairs := []kv{}
	if v := gameVersion(dest); v != "" {
		pairs = append(pairs, kv{"Version", v})
	}
	pairs = append(pairs, kv{"Lancement", os.Args[0] + " \"" + dest + "\""})
	uiKV(2, pairs...)
	return 0
}

// mustAbs : chemin absolu (ou tel quel en cas d'erreur).
func mustAbs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// ── --save et --saves (06/11) ─────────────────────────────────────────────────────

// saveProfileDirs : dossiers du profil Windows où les jeux écrivent leurs
// sauvegardes de partie.
var saveProfileDirs = []string{"Documents", "Saved Games", "AppData/Roaming", "AppData/LocalLow", "AppData/Local",
	"My Documents", "Application Data", "Local Settings/Application Data"}

// gameImage : image du jeu d'après savesDir/.images/<jeu> (dernier lancement) ; "" si inconnue.
func gameImage(name string) string {
	if data, err := os.ReadFile(savesDir + "/.images/" + name); err == nil {
		return strings.TrimRight(string(data), "\n")
	}
	return ""
}

// gameUsesSaveDir : le jeu déclare SAVEDIR ou SAVEFILES (sauvegardes hors du prefix, déjà à leur
// place : ni la copie vers la ludothèque ni la synchro ne les touchent). Image inconnue : faux.
// Pose wsquashfsFile, gameName, autorunOverride et les couches du jeu.
func gameUsesSaveDir(name string) bool {
	img := gameImage(name)
	if img == "" {
		return false
	}
	wsquashfsFile = img
	gameName = name
	autorunOverride = strings.TrimSuffix(img, "/") + ".autorun"
	if !isFile(autorunOverride) {
		autorunOverride = ""
	}
	if pathExists(img) {
		loadLayers(imageAutorun(img))
	} else {
		loadLayers("")
	}
	layerLookup("SAVEDIR", "")
	savedir := lookupVal
	layerLookup("SAVEFILES", "")
	return savedir+lookupVal != ""
}

// copyGameSaves : copie les sauvegardes de partie de l'overlay dans la
// ludothèque (saves/<système>/<jeu>/), sens unique.
func copyGameSaves(name string, g uiGames) {
	saves := savesDir + "/" + name
	img := gameImage(name)
	if img == "" {
		g.line(stSkip, name, "image inconnue (aucun lancement enregistré), ignoré")
		return
	}
	wsquashfsFile = img
	gameName = name
	dest, ok := batoceraSavesDir()
	if !ok {
		g.line(stSkip, name, "hors d'un dossier roms/<système>/ (définir WSQUASHFS_BATOCERA_SAVES), ignoré")
		return
	}
	if gameUsesSaveDir(name) {
		g.line(stSkip, name, "déjà dans "+dest+" (SAVEDIR/SAVEFILES)")
		return
	}
	users := saves + "/drive_c/users"
	if !isDirPath(users) {
		g.line(stSkip, name, "aucune sauvegarde de partie dans l'overlay")
		return
	}
	copied, total, bytesCopied := 0, 0, int64(0)
	for _, f := range collectSaveFiles(users) {
		total++
		src := users + "/" + f.Rel
		rel := "drive_c/users/" + f.Rel
		target := dest + "/" + rel
		// Date à la nanoseconde (collectSaveFiles ne garde que la seconde) : copie si la source est plus récente.
		info, err := os.Lstat(src)
		if err != nil {
			warn(name + " : copie impossible de " + rel)
			continue
		}
		if tst, err := os.Stat(target); err == nil && !info.ModTime().After(tst.ModTime()) {
			continue
		}
		if os.MkdirAll(filepath.Dir(target), 0o755) == nil && copyPreserve(src, target) == nil {
			copied++
			bytesCopied += info.Size()
		} else {
			warn(name + " : copie impossible de " + rel)
		}
	}
	switch {
	case total == 0:
		g.line(stSkip, name, "aucune sauvegarde de partie dans l'overlay")
	case copied > 0:
		chownToTarget(dest)
		g.line(stOK, name, strconv.Itoa(copied)+" fichier(s) copié(s) ("+humanBytes(bytesCopied)+") dans "+dest)
	default:
		g.line(stOK, name, "à jour ("+dest+")")
	}
}

// isCacheDirName : dossiers de caches exclus des sauvegardes.
func isCacheDirName(n string) bool {
	switch n {
	case "Temp", "NVIDIA", "D3DSCache", "DXCache", "INetCache", "CrashDumps":
		return true
	}
	return strings.Contains(strings.ToLower(n), "shadercache")
}

// copyPreserve : copie d'un fichier avec date (cp -p).
func copyPreserve(src, dst string) error {
	if err := runCmd("cp", "-p", "--", src, dst); err != nil {
		return err
	}
	return nil
}

// cmdSave : --save <jeu>, les sauvegardes d'un seul jeu vers la ludothèque.
func cmdSave(args []string) int {
	name := gameNameOf(args[0])
	if !isDirPath(savesDir + "/" + name) {
		die("aucune sauvegarde pour " + name)
	}
	return copySavesTo([]string{name})
}

// cmdSaves : --saves, les sauvegardes de tous les jeux (sans argument).
func cmdSaves() int {
	targets := savedGames()
	if len(targets) == 0 {
		uiStatusLine(stSkip, "Aucune sauvegarde ("+savesDir+")")
		return 0
	}
	return copySavesTo(targets)
}

// copySavesTo : copie les sauvegardes des jeux donnés (--save et --saves).
func copySavesTo(targets []string) int {
	uiTitle("Copie des sauvegardes de partie", "vers la ludothèque (saves/<système>/<jeu>/)")
	g := newUIGames(targets)
	for _, name := range targets {
		if gameRunning(name) {
			g.line(stSkip, name, "en cours, ignoré")
			continue
		}
		copyGameSaves(name, g)
	}
	uiOut("")
	uiNote("Restauration (jeu arrêté) : recopier <ludothèque>/saves/<système>/<jeu>/drive_c")
	uiCode("cp -a <ludothèque>/saves/<système>/<jeu>/drive_c " + savesDir + "/<jeu>/")
	return 0
}

// ── --agent, --pair, --update ──────────────────────────────────────────

// cmdUpdate : met à jour le lanceur depuis GitHub. Le binaire publié
// s'accompagne de wsquashfs-launcher.sha256 (make build) : déjà identique,
// rien n'est téléchargé ; sinon le fichier téléchargé doit avoir cette
// empreinte et un en-tête ELF avant de remplacer le lanceur.
// updateRepo : dépôt GitHub du lanceur publié (binaire et wsquashfs-launcher.sha256 sur main).
const updateRepo = "Gunnm92/wsquashfs-launcher"

// updateBase : base des téléchargements de --update. Le CDN de raw.githubusercontent.com garde
// chaque fichier de main jusqu'à 5 minutes, indépendamment : juste après une publication, il peut
// servir le nouveau binaire avec l'ancienne empreinte (ou l'inverse). On demande donc à l'API le
// dernier commit de main et on télécharge à son adresse, qui ne change jamais : binaire et
// empreinte sont toujours du même commit. API injoignable (limite de requêtes) : main.
// WSQUASHFS_UPDATE_URL remplace le tout (tests, miroir).
func updateBase() string {
	if v := os.Getenv("WSQUASHFS_UPDATE_URL"); v != "" {
		return v
	}
	raw := "https://raw.githubusercontent.com/" + updateRepo + "/"
	if body, err := downloadBytes("https://api.github.com/repos/" + updateRepo + "/commits/main"); err == nil {
		if sha := commitSHA(body); sha != "" {
			return raw + sha
		}
	}
	return raw + "main"
}

// commitSHA : empreinte d'un commit dans une réponse de l'API GitHub (40 caractères hexadécimaux),
// "" si la réponse n'en contient pas.
func commitSHA(body []byte) string {
	var c struct {
		SHA string `json:"sha"`
	}
	if json.Unmarshal(body, &c) != nil || len(c.SHA) != 40 || strings.Trim(c.SHA, "0123456789abcdef") != "" {
		return ""
	}
	return c.SHA
}

func cmdUpdate() int {
	base := updateBase()
	self, err := os.Executable()
	if err != nil {
		die("chemin du lanceur inconnu")
	}
	sum, err := downloadBytes(base + "/wsquashfs-launcher.sha256")
	want := ""
	if fields := strings.Fields(string(sum)); err == nil && len(fields) > 0 {
		want = strings.ToLower(fields[0])
	}
	if len(want) != 64 || strings.Trim(want, "0123456789abcdef") != "" {
		die("somme de contrôle du lanceur introuvable sur " + base + " : rien n'a été modifié")
	}
	if cur, err := fileSHA256(self); err == nil && cur == want {
		uiStatusLine(stOK, "Lanceur déjà à jour : "+self)
		return 0
	}
	tmp, err := os.MkdirTemp("", "wsq-update.")
	if err != nil {
		die("dossier temporaire impossible")
	}
	defer os.RemoveAll(tmp)
	uiStatusLine(stInfo, "Téléchargement depuis "+base+" ...")
	fresh := tmp + "/wsquashfs-launcher"
	if err := downloadTo(base+"/wsquashfs-launcher", fresh); err != nil {
		die("téléchargement du lanceur impossible")
	}
	if got, err := fileSHA256(fresh); err != nil || got != want {
		die("somme de contrôle du lanceur téléchargé incorrecte : rien n'a été modifié " +
			"(cache de GitHub juste après une publication : réessayer dans 5 minutes)")
	}
	data, _ := os.ReadFile(fresh)
	if !bytes.HasPrefix(data, []byte("\x7fELF")) {
		die("lanceur téléchargé invalide : rien n'a été modifié")
	}
	if err := os.Chmod(fresh, 0o755); err != nil {
		die("droits du lanceur téléchargé impossibles")
	}
	// /tmp peut être un autre système de fichiers : rename échouerait. Copie à
	// côté du lanceur, puis renommage (le lanceur en cours garde son fichier).
	if err := copyFile(fresh, self+".new", 0o755); err != nil {
		os.Remove(self + ".new")
		die("remplacement du lanceur impossible : " + err.Error())
	}
	if err := os.Rename(self+".new", self); err != nil {
		os.Remove(self + ".new")
		die("remplacement du lanceur impossible : " + err.Error())
	}
	uiStatusLine(stOK, "Lanceur mis à jour : "+self)
	removeAgentUnits() // démarrage automatique par systemd des anciennes versions
	uiKV(0, kv{"Vérification", os.Args[0] + " --help"})
	return 0
}
