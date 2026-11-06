package main

// Commandes de maintenance (script bash, section 8) : --clean, --clear,
// --umount, --list, --doctor, --info, --show, --pack, --install, --saves,
// --agent, --pair, --update.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// maintenanceMain : dispatch des commandes de maintenance.
func maintenanceMain() int {
	switch action {
	case "clean":
		cmdClean()
	case "clear":
		return cmdClear(cmdArgs)
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
	case "saves":
		return cmdSaves(cmdArgs)
	case "list":
		cmdList()
	case "doctor":
		return cmdDoctorMain(cmdArgs)
	case "agent":
		return cmdAgentMain(cmdArgs)
	case "pair":
		return cmdPair(cmdArgs)
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

// cleanSaves : retire des sauvegardes de chaque jeu les doublons certains :
// fichiers identiques à l'image, puis DLL que Proton recopie à chaque lancement.
func cleanSaves(name string) {
	saves := savesDir + "/" + name
	if savesBusy(name) {
		fmt.Println("  " + name + " : monté ou en cours, ignoré")
		return
	}
	img := ""
	if data, err := os.ReadFile(savesDir + "/.images/" + name); err == nil {
		img = strings.TrimRight(string(data), "\n")
	}
	n, bytesFreed := 0, int64(0)
	switch {
	case img == "" || !pathExists(img):
		fmt.Println("  " + name + " : image inconnue (lancer le jeu une fois), fichiers du jeu gardés")
	case !cmdOK("getfattr", "--version"):
		fmt.Println("  " + name + " : getfattr absent (paquet attr), fichiers du jeu gardés")
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
		if isDirImg || execDiscard("squashfuse", img, mnt) == nil {
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
			fmt.Println("  " + name + " : montage de l'image impossible, fichiers du jeu gardés")
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
	if n > 0 {
		fmt.Printf("  %s : %d doublon(s) retiré(s), %d Mo libérés\n", name, n, bytesFreed/1048576)
	}
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

// cleanBackups : anciennes archives de --backup (retiré), supprimées.
func cleanBackups() {
	dir := envOr("WSQUASHFS_BACKUP_DIR", filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/backups"))
	if !isDirPath(dir) {
		return
	}
	n := 0
	var size int64
	for _, pat := range []string{"*.tar.zst", "*.tar.gz", "*.part"} {
		files, _ := filepath.Glob(dir + "/" + pat)
		for _, f := range files {
			if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
				size += st.Size()
				if os.Remove(f) == nil {
					n++
				}
			}
		}
	}
	_ = os.Remove(dir)
	if n > 0 {
		fmt.Printf("✓ %d archive(s) supprimée(s) (%s, %s)\n", n, humanBytes(size), dir)
	}
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
func cleanCache() {
	unmountAllStale()
	kept := map[string]bool{}
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
		if savesBusy(g) || isMountPoint(cacheDir+"/mnt/"+g) {
			if !kept[g] {
				fmt.Println("  " + g + " : en cours, gardé")
			}
			kept[g] = true
			continue
		}
		_ = os.RemoveAll(d)
	}
	fmt.Println("✓ Copies de travail supprimées")
}

// cmdClean : --clean.
func cmdClean() {
	fmt.Println("Nettoyage des copies de travail et des doublons des sauvegardes...")
	cleanCache()
	cleanBackups()
	for _, g := range savedGames() {
		cleanSaves(g)
	}
	fmt.Println("✓ Sauvegardes gardées dans : " + savesDir)
}

// ── --clear (01/10) ────────────────────────────────────────────────────

// clearSaves : ne garde que drive_c/users des sauvegardes d'un jeu.
func clearSaves(name string) {
	saves := savesDir + "/" + name
	if savesBusy(name) {
		fmt.Println("  " + name + " : monté ou en cours, ignoré")
		return
	}
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
				return
			}
		}
		_ = os.RemoveAll(saves + "/drive_c")
		if isDirPath(saves + "/.users") {
			_ = os.MkdirAll(saves+"/drive_c", 0o755)
			_ = os.Rename(saves+"/.users", saves+"/drive_c/users")
		}
	}
	_ = os.Remove(saves)
	if isDirPath(saves) {
		fmt.Println("  " + name + " : gardé " + duHuman(saves) + " (drive_c/users)")
	} else {
		fmt.Println("  " + name + " : rien à garder, supprimé")
	}
}

// cmdClear : --clear [jeu].
func cmdClear(args []string) int {
	var targets []string
	if len(args) > 0 {
		name := gameNameOf(args[0])
		if !isDirPath(savesDir + "/" + name) {
			fmt.Println("Aucune sauvegarde pour " + name)
			return 0
		}
		targets = []string{name}
	} else {
		targets = savedGames()
	}
	fmt.Printf("--clear ne garde que drive_c/users de %d jeu(x) : scores et réglages\n", len(targets))
	fmt.Println("  enregistrés ailleurs (jeux arcade, dossier du jeu, registre) seront perdus.")
	fmt.Println("  Aucune archive n'est faite (sauvegardes de partie : --saves).")
	if isTerminal() && !confirm("Continuer ?") {
		fmt.Println("Abandon.")
		return 1
	}
	cleanCache()
	for _, g := range targets {
		clearSaves(g)
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
		fmt.Println("Aucun jeu connu (aucun lancement enregistré dans " + savesDir + ")")
		return
	}
	type row struct{ name, state, version, size, image string }
	var rows []row
	wn, ws, wv, wz := 4, 10, 10, 11
	for _, g := range games {
		r := row{version: "-", size: "aucune"}
		img := ""
		if data, err := os.ReadFile(savesDir + "/.images/" + g); err == nil {
			img = strings.TrimRight(string(data), "\n")
		}
		switch {
		case img == "":
			r.image = "inconnue"
		case pathExists(img):
			r.image = img
			r.version = gameVersion(img)
			if r.version == "" {
				r.version = "-"
			}
		default:
			r.image = "introuvable : " + img
		}
		if isDirPath(savesDir + "/" + g) {
			r.size = duHuman(savesDir + "/" + g)
		}
		r.name = g
		if len(r.name) > 32 {
			r.name = r.name[:31] + "…"
		}
		r.state = gameState(g)
		rows = append(rows, r)
		// ${#…} du script : longueur en octets (locale C du harnais).
		wn = max(wn, len(r.name))
		ws = max(ws, len(r.state))
		wv = max(wv, len(r.version))
		wz = max(wz, len(r.size))
	}
	wn, ws, wv, wz = wn+2, ws+2, wv+2, wz+2
	fmt.Println(pad("Jeu", wn) + pad("État", ws) + pad("Version", wv) + pad("Sauvegardes", wz) + "Image")
	for _, r := range rows {
		fmt.Println(pad(r.name, wn) + pad(r.state, ws) + pad(r.version, wv) + pad(r.size, wz) + r.image)
	}
	fmt.Println()
	fmt.Println("Sauvegardes : " + savesDir + " (" + duHuman(savesDir) + " au total)")
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
		if execDiscard("unsquashfs", "-cat", src, "system.reg") == nil {
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
	fmt.Println("Jeu           : " + gameNameOf(src))
	if version == "" {
		version = "non renseignée (GAME_VERSION= dans l'autorun.cmd)"
	}
	fmt.Println("Version       : " + version)
	fmt.Println("Type          : " + typ)
	if cmdLine, ok := overrideVar(strings.TrimSuffix(src, "/")+".autorun", "CMD"); ok {
		fmt.Println("Commande      : " + cmdLine)
	} else if autorun != "" {
		fmt.Println("Commande      : " + keyLine(autorun, "CMD"))
	} else {
		fmt.Println("Commande      : aucun autorun.cmd")
	}
	fmt.Println("Runner        : " + infoRunner(autorun, joined))
	if isFile(strings.TrimSuffix(src, "/") + ".autorun") {
		override := strings.TrimSuffix(src, "/") + ".autorun"
		fmt.Println("Surcharge     : " + filepath.Base(override) + " (" + overrideKeys(override) + ") — non lue par Batocera")
	}
	fmt.Println("Taille        : " + duHuman(src))
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

	fmt.Println("Jeu           : " + gameName)
	fmt.Println("Couches (de la plus faible à la plus forte) :")
	machine := machineLayer
	if machine == "" {
		machine = machineLayerFile() + " (absent)"
	}
	fmt.Println("  machine     : " + machine)
	folder := folderLayer
	if folder == "" {
		folder = filepath.Dir(src) + "/_defaults.autorun (absent)"
	}
	fmt.Println("  dossier     : " + folder)
	fmt.Println("  image       : autorun.cmd")
	surcharge := strings.TrimSuffix(src, "/") + ".autorun"
	if autorunOverride == "" {
		surcharge += " (absent)"
	}
	fmt.Println("  surcharge   : " + surcharge)
	if len(setArgs) > 0 {
		fmt.Println("  --set       : " + strings.Join(setArgs, " "))
	}
	fmt.Println()
	fmt.Println("Réglages :")
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
	for _, key := range append(append([]string{}, showKeys...), extra...) {
		layerLookup(key, "")
		if lookupVal == "" && lookupSrc == "défaut" {
			continue
		}
		fmt.Printf("  %-16s = %-28s (%s)\n", key, lookupVal, lookupSrc)
	}
	if envLayers > 0 {
		fmt.Println("  ENV :")
		if mergedEnv() == "" {
			fmt.Println("    (aucune variable : ENV= vide)")
		}
		seen := map[string]bool{}
		for _, n := range envOrder {
			if _, ok := envVal[n]; !ok || seen[n] {
				continue
			}
			seen[n] = true
			fmt.Printf("    %-42s (%s)\n", envVal[n], envSrc[n])
		}
	}
	fmt.Println()
	fmt.Println("Overlay       : " + savesDir + "/" + gameName)
	layerLookup("SAVEDIR", "")
	key := lookupVal
	layerLookup("SAVEFILES", "")
	sysdir, ok := batoceraSavesDir()
	switch {
	case !ok:
		fmt.Println("Sauvegardes   : hors d'un dossier roms/<système>/ (WSQUASHFS_BATOCERA_SAVES)")
	case key+lookupVal != "":
		fmt.Println("Sauvegardes   : " + sysdir + " (SAVEDIR/SAVEFILES)")
	default:
		fmt.Println("Sauvegardes   : dans l'overlay ; --saves les copie dans " + sysdir)
	}
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
			fmt.Println("Abandon.")
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
	fmt.Println("Création de " + out + " depuis " + src + "...")
	if err := runCmd("mksquashfs", src, tmp, "-comp", "zstd", "-noappend", "-quiet"); err != nil {
		_ = os.Remove(tmp)
		die("échec de mksquashfs, " + out + " inchangé")
	}
	_ = os.Rename(tmp, out)
	if resetSaves {
		if os.RemoveAll(saves) == nil {
			fmt.Println("Sauvegardes réinitialisées : " + saves)
		}
	}
	fmt.Println("✓ " + out + " (" + duHuman(out) + ")")
	if v := gameVersion(src); v != "" {
		fmt.Println("  Version : " + v)
	} else if isFile(src + "/autorun.cmd") {
		fmt.Println("  Version non renseignée (GAME_VERSION=... dans l'autorun.cmd, voir --info)")
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
	fmt.Println("Installation de " + name + " dans " + dest + "...")
	if err := runCmd("unsquashfs", "-no-xattrs", "-d", tmp, img); err != nil {
		_ = os.RemoveAll(tmp)
		die("échec de l'extraction, rien n'a été installé")
	}
	_ = os.Rename(tmp, dest)
	if dropsToTargetUser() {
		_ = runCmd("chown", "-R", fmt.Sprintf("%d:%d", wineUID, wineGID), dest)
	}
	fmt.Println("✓ " + dest + " (" + duHuman(dest) + ")")
	if v := gameVersion(dest); v != "" {
		fmt.Println("  Version : " + v)
	}
	fmt.Println("  Lancement : " + os.Args[0] + " \"" + dest + "\"")
	return 0
}

// mustAbs : chemin absolu (ou tel quel en cas d'erreur).
func mustAbs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// ── --saves (06/11) ────────────────────────────────────────────────────

// saveProfileDirs : dossiers du profil Windows où les jeux écrivent leurs
// sauvegardes de partie.
var saveProfileDirs = []string{"Documents", "Saved Games", "AppData/Roaming", "AppData/LocalLow", "AppData/Local",
	"My Documents", "Application Data", "Local Settings/Application Data"}

// copyGameSaves : copie les sauvegardes de partie de l'overlay dans la
// ludothèque (saves/<système>/<jeu>/), sens unique.
func copyGameSaves(name string) {
	saves := savesDir + "/" + name
	img := ""
	if data, err := os.ReadFile(savesDir + "/.images/" + name); err == nil {
		img = strings.TrimRight(string(data), "\n")
	}
	if img == "" {
		fmt.Println("  " + name + " : image inconnue (aucun lancement enregistré), ignoré")
		return
	}
	wsquashfsFile = img
	gameName = name
	dest, ok := batoceraSavesDir()
	if !ok {
		fmt.Println("  " + name + " : hors d'un dossier roms/<système>/ (définir WSQUASHFS_BATOCERA_SAVES), ignoré")
		return
	}
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
	if savedir+lookupVal != "" {
		fmt.Println("  " + name + " : déjà dans " + dest + " (SAVEDIR/SAVEFILES)")
		return
	}
	users := saves + "/drive_c/users"
	if !isDirPath(users) {
		fmt.Println("  " + name + " : aucune sauvegarde de partie dans l'overlay")
		return
	}
	copied, total, bytesCopied := 0, 0, int64(0)
	entries, _ := os.ReadDir(users)
	for _, u := range entries {
		switch u.Name() {
		case "Public", "All Users", "Default User", "LocalService", "NetworkService":
			continue
		}
		udir := users + "/" + u.Name()
		for _, sub := range saveProfileDirs {
			src := udir + "/" + sub
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
				rel := strings.TrimPrefix(f, saves+"/")
				total++
				target := dest + "/" + rel
				if tst, err := os.Stat(target); err == nil && !info.ModTime().After(tst.ModTime()) {
					return nil
				}
				if os.MkdirAll(filepath.Dir(target), 0o755) == nil && copyPreserve(f, target) == nil {
					copied++
					bytesCopied += info.Size()
				} else {
					warn(name + " : copie impossible de " + rel)
				}
				return nil
			})
		}
	}
	switch {
	case total == 0:
		fmt.Println("  " + name + " : aucune sauvegarde de partie dans l'overlay")
	case copied > 0:
		chownToTarget(dest)
		fmt.Println("  " + name + " : " + strconv.Itoa(copied) + " fichier(s) copié(s) (" + humanBytes(bytesCopied) + ") dans " + dest)
	default:
		fmt.Println("  " + name + " : à jour (" + dest + ")")
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

// cmdSaves : --saves [jeu].
func cmdSaves(args []string) int {
	var targets []string
	if len(args) > 0 {
		name := gameNameOf(args[0])
		if !isDirPath(savesDir + "/" + name) {
			die("aucune sauvegarde pour " + name)
		}
		targets = []string{name}
	} else {
		targets = savedGames()
	}
	if len(targets) == 0 {
		fmt.Println("Aucune sauvegarde (" + savesDir + ")")
		return 0
	}
	fmt.Println("Copie des sauvegardes de partie dans la ludothèque...")
	for _, g := range targets {
		if gameRunning(g) {
			fmt.Println("  " + g + " : en cours, ignoré")
			continue
		}
		copyGameSaves(g)
	}
	fmt.Println("Restauration (jeu arrêté) : recopier <ludothèque>/saves/<système>/<jeu>/drive_c")
	fmt.Println("  dans " + savesDir + "/<jeu>/ (cp -a)")
	return 0
}

// ── --agent, --pair, --update ──────────────────────────────────────────

// cmdUpdate : met à jour le lanceur depuis GitHub. Le fichier téléchargé est
// vérifié (script bash : bash -n ; binaire : en-tête ELF) avant remplacement.
func cmdUpdate() int {
	base := envOr("WSQUASHFS_UPDATE_URL", "https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main")
	self, err := os.Executable()
	if err != nil {
		die("chemin du lanceur inconnu")
	}
	tmp, err := os.MkdirTemp("", "wsq-update.")
	if err != nil {
		die("dossier temporaire impossible")
	}
	defer os.RemoveAll(tmp)
	fmt.Println("Téléchargement depuis " + base + " ...")
	fresh := tmp + "/wsquashfs-launcher"
	if err := downloadTo(base+"/wsquashfs-launcher", fresh); err != nil {
		die("téléchargement du lanceur impossible")
	}
	data, _ := os.ReadFile(fresh)
	switch {
	case bytes.HasPrefix(data, []byte("\x7fELF")):
	case bytes.HasPrefix(data, []byte("#!")):
		if runCmd("bash", "-n", fresh) != nil {
			die("lanceur téléchargé invalide : rien n'a été modifié")
		}
	default:
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
	fmt.Println("✓ Lanceur mis à jour : " + self)
	fmt.Println("Vérification : " + os.Args[0] + " --help")
	return 0
}
