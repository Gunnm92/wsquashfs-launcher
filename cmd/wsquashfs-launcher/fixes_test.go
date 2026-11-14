package main

// Tests des corrections de l'audit du 12/11 : SAVEDIR / SAVEFILES entre deux
// systèmes de fichiers, marque des DLL de Wine, verrou de maintenance,
// archive d'un runner et nom de jeu.

import (
	"archive/tar"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// otherFilesystem : dossier sur un autre système de fichiers que t.TempDir()
// (le prefix d'un jeu est un montage : rename vers la ludothèque y échoue).
func otherFilesystem(t *testing.T, disk string) string {
	t.Helper()
	shm, err := os.MkdirTemp("/dev/shm", "wsq-test.")
	if err != nil {
		t.Skip("/dev/shm indisponible : " + err.Error())
	}
	t.Cleanup(func() { os.RemoveAll(shm) })
	a, errA := os.Stat(shm)
	b, errB := os.Stat(disk)
	if errA != nil || errB != nil || a.Sys().(*syscall.Stat_t).Dev == b.Sys().(*syscall.Stat_t).Dev {
		t.Skip("/dev/shm et le dossier temporaire sont sur le même système de fichiers")
	}
	return shm
}

func TestSavefilesMovedAcrossFilesystems(t *testing.T) {
	disk := t.TempDir()
	winePoint = otherFilesystem(t, disk)
	gameName = "Jeu"
	t.Setenv("WSQUASHFS_BATOCERA_SAVES", disk)
	save := winePoint + "/drive_c/game/score.dat"
	if err := os.MkdirAll(filepath.Dir(save), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(save, []byte("MES SCORES"), 0o644); err != nil {
		t.Fatal(err)
	}
	applySavedir("", `drive_c\game\score.dat`)

	moved := filepath.Join(disk, "Jeu/drive_c/game/score.dat")
	if data, err := os.ReadFile(moved); err != nil || string(data) != "MES SCORES" {
		t.Fatalf("sauvegarde absente de la ludothèque : %q, %v", data, err)
	}
	if link, err := os.Readlink(save); err != nil || link != moved {
		t.Errorf("le prefix ne pointe pas sur la ludothèque : %q, %v", link, err)
	}
	if data, _ := os.ReadFile(save); string(data) != "MES SCORES" {
		t.Errorf("sauvegarde illisible depuis le prefix : %q", data)
	}
}

func TestSavefilesKeptWhenMoveFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root écrit partout")
	}
	disk := t.TempDir()
	winePoint = otherFilesystem(t, disk)
	gameName = "Jeu"
	t.Setenv("WSQUASHFS_BATOCERA_SAVES", disk)
	save := winePoint + "/drive_c/game/score.dat"
	_ = os.MkdirAll(filepath.Dir(save), 0o755)
	_ = os.WriteFile(save, []byte("MES SCORES"), 0o644)
	// Ludothèque en lecture seule : le déplacement échoue.
	dest := filepath.Join(disk, "Jeu/drive_c/game")
	_ = os.MkdirAll(dest, 0o755)
	_ = os.Chmod(dest, 0o555)
	t.Cleanup(func() { os.Chmod(dest, 0o755) })
	applySavedir("", "drive_c/game/score.dat")

	if st, err := os.Lstat(save); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("sauvegarde retirée du prefix alors que le déplacement a échoué (%v)", err)
	}
	if data, _ := os.ReadFile(save); string(data) != "MES SCORES" {
		t.Errorf("sauvegarde modifiée : %q", data)
	}
}

func TestSavedirMovedAndKeptOnFailure(t *testing.T) {
	disk := t.TempDir()
	winePoint = otherFilesystem(t, disk)
	gameName = "Jeu"
	t.Setenv("WSQUASHFS_BATOCERA_SAVES", disk)
	dir := winePoint + "/drive_c/Saves"
	_ = os.MkdirAll(dir+"/slot", 0o755)
	_ = os.WriteFile(dir+"/slot/a.sav", []byte("A"), 0o644)
	// Déjà dans la ludothèque : jamais écrasé.
	_ = os.MkdirAll(filepath.Join(disk, "Jeu"), 0o755)
	_ = os.WriteFile(filepath.Join(disk, "Jeu/keep.sav"), []byte("LUDO"), 0o644)
	_ = os.WriteFile(dir+"/keep.sav", []byte("PREFIX"), 0o644)
	applySavedir(`drive_c\Saves`, "")

	if data, _ := os.ReadFile(filepath.Join(disk, "Jeu/slot/a.sav")); string(data) != "A" {
		t.Errorf("fichier du dossier non copié : %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(disk, "Jeu/keep.sav")); string(data) != "LUDO" {
		t.Errorf("fichier de la ludothèque écrasé : %q", data)
	}
	if link, err := os.Readlink(dir); err != nil || link != filepath.Join(disk, "Jeu") {
		t.Errorf("SAVEDIR n'est pas un lien vers la ludothèque : %q, %v", link, err)
	}

	if os.Geteuid() == 0 {
		return
	}
	// Copie impossible : le dossier reste un vrai dossier, avec son contenu.
	gameName = "Autre"
	dir2 := winePoint + "/drive_c/Saves2"
	_ = os.MkdirAll(dir2+"/slot", 0o755)
	_ = os.WriteFile(dir2+"/slot/b.sav", []byte("B"), 0o644)
	blocked := filepath.Join(disk, "Autre")
	_ = os.MkdirAll(blocked, 0o555)
	t.Cleanup(func() { os.Chmod(blocked, 0o755) })
	applySavedir("drive_c/Saves2", "")
	if data, _ := os.ReadFile(dir2 + "/slot/b.sav"); string(data) != "B" {
		t.Errorf("dossier de sauvegardes perdu après une copie en échec : %q", data)
	}
	if st, err := os.Lstat(dir2); err != nil || !st.IsDir() {
		t.Errorf("SAVEDIR remplacé par un lien malgré l'échec (%v)", err)
	}
}

func TestWineOwnedReadsHeaderOnly(t *testing.T) {
	dir := t.TempDir()
	header := make([]byte, 64)
	builtin := append(append([]byte{}, header...), []byte("Wine builtin DLL\x00")...)
	placeholder := append(append([]byte{}, header...), []byte("Wine placeholder DLL\x00")...)
	// Marque loin dans le fichier : une DLL native qui cite Wine n'est pas à lui.
	native := append(make([]byte, 4096), []byte("Wine builtin DLL")...)
	for name, data := range map[string][]byte{"b.dll": builtin, "p.dll": placeholder, "n.dll": native, "court.dll": []byte("MZ")} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]bool{"b.dll": true, "p.dll": true, "n.dll": false, "court.dll": false, "absent.dll": false} {
		if got := wineOwned(filepath.Join(dir, name)); got != want {
			t.Errorf("wineOwned(%s) = %v, attendu %v", name, got, want)
		}
	}
}

func TestLockIdleGameRefusesLockedGame(t *testing.T) {
	dir := t.TempDir()
	cacheDir, savesDir = filepath.Join(dir, "cache"), filepath.Join(dir, "saves")
	lockedGame = ""
	lock, idle := lockIdleGame("Jeu")
	if !idle || lock == nil {
		t.Fatalf("jeu arrêté non verrouillé (idle=%v)", idle)
	}
	// Un lancement pendant la maintenance : verrou refusé (« déjà lancé »).
	gameName = "Jeu"
	if acquireGameLock() {
		t.Error("lancement accepté pendant une maintenance")
	}
	// Une seconde maintenance : refusée aussi.
	if second, idle := lockIdleGame("Jeu"); idle {
		second.Close()
		t.Error("deux maintenances du même jeu en même temps")
	}
	lock.Close()
	if !acquireGameLock() {
		t.Fatal("lancement refusé après la maintenance")
	}
	defer func() { lockFile.Close(); lockFile, lockedGame = nil, "" }()
	// Jeu verrouillé par un lancement : la maintenance le laisse.
	lockedGame = ""
	if l, idle := lockIdleGame("Jeu"); idle {
		l.Close()
		t.Error("maintenance acceptée sur un jeu en cours de lancement")
	}
}

func TestRunnerTarSkipsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root lit tout")
	}
	_, h, _ := agentFixture(t)
	sysroot = ""
	runner := filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/wine/wine-test")
	_ = os.MkdirAll(runner+"/bin", 0o755)
	_ = os.WriteFile(runner+"/bin/wine", []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(runner+"/a-secret.bin", []byte("illisible, plus long que la suite"), 0o000)
	_ = os.WriteFile(runner+"/z-last.txt", []byte("fin"), 0o644)

	rec := do(h, "GET", "/v1/runner/tar?path="+runner, "secret-token", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	names := map[string]string{}
	tr := tar.NewReader(rec.Body)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("archive illisible après %d entrées : %v", len(names), err)
		}
		data, _ := io.ReadAll(tr)
		names[hdr.Name] = string(data)
	}
	if _, ok := names["wine-test/a-secret.bin"]; ok {
		t.Error("fichier illisible présent dans l'archive")
	}
	if names["wine-test/z-last.txt"] != "fin" {
		t.Errorf("fichier suivant le fichier illisible absent ou faux : %q", names["wine-test/z-last.txt"])
	}
}

func TestIsMountPointPlainDirs(t *testing.T) {
	dir := t.TempDir()
	if isMountPoint(dir) {
		t.Error("dossier ordinaire vu comme un montage")
	}
	if isMountPoint(filepath.Join(dir, "absent")) {
		t.Error("chemin absent vu comme un montage")
	}
}

// ── --clear / --erase (12/11) ──────────────────────────────────────────

func TestKeepOnlyUsers(t *testing.T) {
	saves := filepath.Join(t.TempDir(), "Jeu")
	for _, f := range []string{"system.reg", "drive_c/windows/system32/a.dll", "drive_c/Game/score.dat", "drive_c/users/u/Documents/slot.sav"} {
		_ = os.MkdirAll(filepath.Dir(saves+"/"+f), 0o755)
		_ = os.WriteFile(saves+"/"+f, []byte(f), 0o644)
	}
	if !keepOnlyUsers(saves) {
		t.Fatal("dossier supprimé alors que drive_c/users existe")
	}
	if data, _ := os.ReadFile(saves + "/drive_c/users/u/Documents/slot.sav"); len(data) == 0 {
		t.Error("sauvegarde de partie perdue")
	}
	for _, gone := range []string{"system.reg", "drive_c/windows", "drive_c/Game", ".users"} {
		if pathExists(saves + "/" + gone) {
			t.Errorf("%s gardé", gone)
		}
	}
	// Rien dans users : le dossier du jeu disparaît.
	empty := filepath.Join(t.TempDir(), "Vide")
	_ = os.MkdirAll(empty+"/drive_c/windows", 0o755)
	if keepOnlyUsers(empty) || pathExists(empty) {
		t.Error("couche sans drive_c/users gardée")
	}
}

func TestPruneBackupsKeepsLatestPerGame(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WSQUASHFS_BACKUP_DIR", dir)
	files := []string{
		"Jeu-20261101-100000.tar.zst", "Jeu-20261102-100000.tar.zst", "Jeu-20261103-100000.tar.zst",
		"Jeu-20261103-100000-2.tar.zst", "Jeu-20261104-100000.tar.gz",
		"Jeu-2-20261101-100000.tar.zst", "Autre-20250101-000000.tar.zst",
		"Jeu-20261105-100000.tar.zst.part", "notes.txt",
	}
	for _, f := range files {
		_ = os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	pruneBackups()
	want := map[string]bool{
		// Les 3 dernières de « Jeu » (le suffixe -2 est plus récent à date égale).
		"Jeu-20261104-100000.tar.gz": true, "Jeu-20261103-100000-2.tar.zst": true, "Jeu-20261103-100000.tar.zst": true,
		// Un autre jeu, dont le nom finit par -2 : jamais confondu avec « Jeu ».
		"Jeu-2-20261101-100000.tar.zst": true, "Autre-20250101-000000.tar.zst": true,
		"notes.txt": true,
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !want[e.Name()] {
			t.Errorf("%s gardé à tort", e.Name())
		}
		delete(want, e.Name())
	}
	for name := range want {
		t.Errorf("%s supprimé à tort", name)
	}
}

func TestEraseSavesArchivesThenRemoves(t *testing.T) {
	dir := t.TempDir()
	cacheDir, savesDir = filepath.Join(dir, "cache"), filepath.Join(dir, "saves")
	backups := filepath.Join(dir, "backups")
	t.Setenv("WSQUASHFS_BACKUP_DIR", backups)
	lockedGame = ""
	save := savesDir + "/Jeu/drive_c/users/u/Documents/slot.sav"
	_ = os.MkdirAll(filepath.Dir(save), 0o755)
	_ = os.WriteFile(save, []byte("PARTIE"), 0o644)
	_ = os.WriteFile(savesDir+"/Jeu/system.reg", []byte("reg"), 0o644)
	eraseSaves("Jeu", newUIGames([]string{"Jeu"}))

	if pathExists(savesDir + "/Jeu") {
		t.Error("sauvegardes non supprimées")
	}
	archives, _ := filepath.Glob(backups + "/Jeu-*.tar.*")
	if len(archives) != 1 {
		t.Fatalf("archives : %v", archives)
	}
	// Restauration documentée : tar -xf <archive> -C saves/<jeu>/
	restore := filepath.Join(dir, "restore")
	_ = os.MkdirAll(restore, 0o755)
	if err := execQuiet("tar", "-xf", archives[0], "-C", restore); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(restore + "/drive_c/users/u/Documents/slot.sav"); string(data) != "PARTIE" {
		t.Errorf("archive sans la sauvegarde : %q", data)
	}
	if pathExists(restore + "/system.reg") {
		t.Error("l'archive contient plus que drive_c/users")
	}
}

func TestEraseSavesKeepsSavesWhenArchiveFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root écrit partout")
	}
	dir := t.TempDir()
	cacheDir, savesDir = filepath.Join(dir, "cache"), filepath.Join(dir, "saves")
	blocked := filepath.Join(dir, "lecture-seule")
	_ = os.MkdirAll(blocked, 0o555)
	t.Cleanup(func() { os.Chmod(blocked, 0o755) })
	t.Setenv("WSQUASHFS_BACKUP_DIR", filepath.Join(blocked, "backups"))
	lockedGame = ""
	save := savesDir + "/Jeu/drive_c/users/u/slot.sav"
	_ = os.MkdirAll(filepath.Dir(save), 0o755)
	_ = os.WriteFile(save, []byte("PARTIE"), 0o644)
	eraseSaves("Jeu", newUIGames([]string{"Jeu"}))
	if data, _ := os.ReadFile(save); string(data) != "PARTIE" {
		t.Error("sauvegardes supprimées alors que l'archive a échoué")
	}
}

func TestCopyUsersStashAndRestore(t *testing.T) {
	dir := t.TempDir()
	cacheDir = filepath.Join(dir, "cache")
	gameName = "Jeu"
	winePoint = filepath.Join(cacheDir, "wine", "Jeu")
	save := winePoint + "/drive_c/users/u/Documents/slot.sav"
	_ = os.MkdirAll(filepath.Dir(save), 0o755)
	_ = os.WriteFile(save, []byte("PARTIE"), 0o644)

	// Copie incomplète (pas de marqueur) : rien à sauver, rien mis de côté.
	if !stashCopyUsers(winePoint, "Jeu") || pathExists(copyUsersStash("Jeu")) {
		t.Fatal("copie sans marqueur mise de côté")
	}
	_ = os.WriteFile(winePoint+"/.wsquashfs_cache", []byte("sig"), 0o644)
	if !stashCopyUsers(winePoint, "Jeu") {
		t.Fatal("mise de côté en échec")
	}
	_ = os.RemoveAll(winePoint)

	// Copie refaite depuis l'image : son drive_c/users reçoit les sauvegardes.
	_ = os.MkdirAll(winePoint+"/drive_c/users/u/Documents", 0o755)
	_ = os.WriteFile(winePoint+"/drive_c/users/u/Documents/defaut.ini", []byte("image"), 0o644)
	_ = os.WriteFile(save, []byte("IMAGE"), 0o644)
	restoreCopyUsers()
	if data, _ := os.ReadFile(save); string(data) != "PARTIE" {
		t.Errorf("sauvegarde non reprise : %q", data)
	}
	if pathExists(copyUsersStash("Jeu")) {
		t.Error("mise de côté gardée après la reprise")
	}
}

func TestParseArgsMaintenanceActions(t *testing.T) {
	for _, flag := range []string{"--clean", "--clear", "--erase"} {
		o, err := parseArgs([]string{flag, "Jeu"})
		if err != nil || o.action != flag[2:] {
			t.Errorf("%s : action %v, erreur %v", flag, o, err)
		}
		if launchesGame([]string{flag}) {
			t.Errorf("%s compté comme un lancement de jeu", flag)
		}
	}

	// --save <jeu> : un seul jeu, action interne « save » (agent, manager).
	o, err := parseArgs([]string{"--save", "Jeu"})
	if err != nil {
		t.Fatalf("--save Jeu : erreur %v", err)
	}
	if o.action != "save" || len(o.cmdArgs) != 1 || o.cmdArgs[0] != "Jeu" {
		t.Errorf("--save Jeu : action %q, cmdArgs %v", o.action, o.cmdArgs)
	}
	if launchesGame([]string{"--save", "Jeu"}) {
		t.Error("--save compté comme un lancement de jeu")
	}
	// --save sans argument (ou vide) : erreur d'usage.
	for _, args := range [][]string{{"--save"}, {"--save", ""}} {
		_, err := parseArgs(args)
		var ae *argError
		if !errors.As(err, &ae) || !ae.usage {
			t.Errorf("%q : erreur %v, attendue une erreur d'usage", args, err)
		}
	}
	// --saves seul : tous les jeux, action « saves ».
	o, err = parseArgs([]string{"--saves"})
	if err != nil || o.action != "saves" || len(o.cmdArgs) != 0 {
		t.Errorf("--saves : action %v, erreur %v", o, err)
	}
	if launchesGame([]string{"--saves"}) {
		t.Error("--saves compté comme un lancement de jeu")
	}
	// --saves avec un jeu : plus d'alias, erreur d'usage qui renvoie à --save.
	for _, args := range [][]string{{"--saves", "Jeu"}, {"--saves", ""}} {
		_, err := parseArgs(args)
		var ae *argError
		if !errors.As(err, &ae) || !ae.usage || !strings.Contains(ae.msg, "--save <jeu>") {
			t.Errorf("%q : erreur %v, attendue une erreur d'usage vers --save <jeu>", args, err)
		}
	}
	// --sync : action « sync » (agent).
	if o, err := parseArgs([]string{"--sync"}); err != nil || o.action != "sync" {
		t.Errorf("--sync : action %v, erreur %v", o, err)
	}
}

func TestCommitSHA(t *testing.T) {
	sha := "bf66458e0123456789abcdef0123456789abcdef"[:40]
	cases := map[string]string{
		`{"sha":"` + sha + `","commit":{}}`:                  sha,
		`{"sha":"BF66458E0123456789ABCDEF0123456789ABCDEF"}`: "", // l'API répond en minuscules
		`{"sha":"bf66458"}`:                                  "",
		`{"message":"API rate limit exceeded"}`:              "",
		`pas du json`:                                        "",
		`{"sha":"../../evil/0123456789abcdef0123456789ab"}`:  "",
	}
	for body, want := range cases {
		if got := commitSHA([]byte(body)); got != want {
			t.Errorf("commitSHA(%s) = %q, attendu %q", body, got, want)
		}
	}
}

func TestUpdateBaseHonoursOverride(t *testing.T) {
	t.Setenv("WSQUASHFS_UPDATE_URL", "http://miroir.local/x")
	if got := updateBase(); got != "http://miroir.local/x" {
		t.Fatalf("updateBase = %q", got)
	}
}

// TestSetupOptionsWithoutDashes : les options de --setup s'écrivent sans tirets (comme --agent stop,
// --sync status) ; les anciennes formes (--yes, -y, --agent…) sont refusées sans rien installer.
func TestSetupOptionsWithoutDashes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Cleanup(func() { assumeYes, dryRun = false, false })
	capteUI(t)
	for _, old := range []string{"--yes", "-y", "--dry-run", "-n", "--uninstall", "-u", "--help", "-h", "--agent", "agent"} {
		if code := runSetup([]string{old}); code != 1 {
			t.Errorf("--setup %s : code %d, attendu 1 (option refusée)", old, code)
		}
	}
	if code := runSetup([]string{"help"}); code != 0 {
		t.Errorf("--setup help : code %d", code)
	}
}
