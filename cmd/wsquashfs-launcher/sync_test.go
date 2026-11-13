package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// syncTestTime : date fixe des fichiers de test.
var syncTestTime = time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)

// syncTestPut : écrit un fichier avec une date fixe.
func syncTestPut(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// syncTestPopulate : users/ d'une machine avec le profil donné, les sauvegardes du périmètre et
// des fichiers hors périmètre (cache, Public, .wh., dossier non sauvegarde, lien).
func syncTestPopulate(t *testing.T, users, profile string) {
	t.Helper()
	p := filepath.Join(users, profile)
	syncTestPut(t, filepath.Join(p, "Documents", "Game", "save1.sav"), "sauvegarde", syncTestTime)
	syncTestPut(t, filepath.Join(p, "AppData", "Local", "Game", "cfg.ini"), "cfg", syncTestTime.Add(time.Hour))
	syncTestPut(t, filepath.Join(p, "Temp", "x"), "temp", syncTestTime)
	syncTestPut(t, filepath.Join(p, "AppData", "Local", "Temp", "cache.tmp"), "tmp", syncTestTime)
	syncTestPut(t, filepath.Join(p, "Documents", "shadercache_x", "s.bin"), "shader", syncTestTime)
	syncTestPut(t, filepath.Join(p, "Documents", ".wh.x"), "wh", syncTestTime)
	syncTestPut(t, filepath.Join(p, "Pictures", "p.png"), "png", syncTestTime)
	syncTestPut(t, filepath.Join(users, "Public", "Documents", "pub.sav"), "public", syncTestTime)
	if err := os.Symlink(filepath.Join(p, "Pictures", "p.png"), filepath.Join(p, "Documents", "lien.sav")); err != nil {
		t.Fatal(err)
	}
}

// syncTestTree : chemin -> contenu et date, pour vérifier qu'un dossier n'a pas changé.
func syncTestTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(root, func(f string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, f)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(f)
			out[rel] = "->" + target
		case info.IsDir():
			out[rel] = "dir"
		default:
			data, _ := os.ReadFile(f)
			out[rel] = string(data) + "@" + info.ModTime().String()
		}
		return nil
	})
	return out
}

// syncTestArchive : archive tar.gz brute, pour simuler une archive malveillante.
func syncTestArchive(t *testing.T, hdrs ...*tar.Header) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range hdrs {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte("evil")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestSyncCollectPerimeter(t *testing.T) {
	users := filepath.Join(t.TempDir(), "users")
	syncTestPopulate(t, users, "deck")
	files := collectSaveFiles(users)
	var got []string
	for _, f := range files {
		got = append(got, f.Rel)
	}
	want := []string{"deck/AppData/Local/Game/cfg.ini", "deck/Documents/Game/save1.sav"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("périmètre = %v, attendu %v", got, want)
	}
	if files[1].Size != int64(len("sauvegarde")) || files[1].MTime != syncTestTime.Unix() {
		t.Errorf("taille ou date inattendues : %+v", files[1])
	}
	if collectSaveFiles(filepath.Join(t.TempDir(), "absent")) != nil {
		t.Error("dossier absent : attendu nil")
	}
}

func TestSyncFingerprint(t *testing.T) {
	a := filepath.Join(t.TempDir(), "users")
	b := filepath.Join(t.TempDir(), "users")
	syncTestPopulate(t, a, "deck")
	syncTestPopulate(t, b, "arcade")
	fa := saveFingerprint(a, collectSaveFiles(a))
	fb := saveFingerprint(b, collectSaveFiles(b))
	if fa == "" || fa != fb {
		t.Fatalf("empreintes A=%q B=%q : attendues identiques malgré les profils", fa, fb)
	}
	if saveFingerprint(a, nil) != "" {
		t.Error("sans fichier : attendu \"\"")
	}

	// Changement de taille, puis de date : l'empreinte change.
	syncTestPut(t, filepath.Join(b, "arcade", "Documents", "Game", "save1.sav"), "sauvegarde+", syncTestTime)
	if fb2 := saveFingerprint(b, collectSaveFiles(b)); fb2 == fb {
		t.Error("taille modifiée : l'empreinte n'a pas changé")
	}
	syncTestPut(t, filepath.Join(b, "arcade", "Documents", "Game", "save1.sav"), "sauvegarde", syncTestTime.Add(time.Second))
	fb3 := saveFingerprint(b, collectSaveFiles(b))
	if fb3 == fb {
		t.Error("date modifiée : l'empreinte n'a pas changé")
	}
	if newestSave(collectSaveFiles(b)) != syncTestTime.Add(time.Hour).Unix() {
		t.Errorf("newestSave = %d", newestSave(collectSaveFiles(b)))
	}
	if newestSave(nil) != 0 {
		t.Error("newestSave(vide) : attendu 0")
	}
}

func TestSyncRoundTrip(t *testing.T) {
	root := t.TempDir()
	usersA := filepath.Join(root, "A", "users")
	usersB := filepath.Join(root, "B", "users")
	syncTestPopulate(t, usersA, "deck")
	// B : profil arcade, une sauvegarde propre à B (à supprimer), des fichiers hors périmètre (à garder).
	syncTestPopulate(t, usersB, "arcade")
	syncTestPut(t, filepath.Join(usersB, "arcade", "Documents", "Game", "only_b.sav"), "b", syncTestTime)
	syncTestPut(t, filepath.Join(usersB, "arcade", "Temp", "garde.txt"), "garde", syncTestTime)
	if err := os.Remove(filepath.Join(usersB, "arcade", "Documents", "Game", "save1.sav")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	profile, err := writeSaveArchive(&buf, usersA, collectSaveFiles(usersA))
	if err != nil {
		t.Fatal(err)
	}
	if profile != "deck" {
		t.Errorf("profil de l'archive = %q, attendu deck", profile)
	}
	// Le profil seul devient le token dans l'archive.
	gz, _ := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if !strings.HasPrefix(h.Name, syncProfileToken+"/") {
			t.Errorf("entrée %q : attendue sous le token", h.Name)
		}
	}

	if err := applySaveArchive(bytes.NewReader(buf.Bytes()), usersB, "deck"); err != nil {
		t.Fatalf("applySaveArchive : %v", err)
	}
	got, err := os.ReadFile(filepath.Join(usersB, "arcade", "Documents", "Game", "save1.sav"))
	if err != nil || string(got) != "sauvegarde" {
		t.Fatalf("save1.sav sur B = %q, %v", got, err)
	}
	st, err := os.Stat(filepath.Join(usersB, "arcade", "AppData", "Local", "Game", "cfg.ini"))
	if err != nil || st.ModTime().Unix() != syncTestTime.Add(time.Hour).Unix() {
		t.Errorf("date de cfg.ini sur B : %v, %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(usersB, "arcade", "Documents", "Game", "only_b.sav")); !os.IsNotExist(err) {
		t.Error("only_b.sav (dans le périmètre de B) : attendu supprimé")
	}
	if _, err := os.Stat(filepath.Join(usersB, "arcade", "Temp", "garde.txt")); err != nil {
		t.Errorf("Temp de B (hors périmètre) : attendu gardé : %v", err)
	}
	if _, err := os.Stat(filepath.Join(usersB, "Public", "Documents", "pub.sav")); err != nil {
		t.Errorf("Public : attendu intact : %v", err)
	}
	if entries, _ := os.ReadDir(root + "/B"); len(entries) != 1 {
		t.Errorf("dossier temporaire resté : %v", entries)
	}
	fa := saveFingerprint(usersA, collectSaveFiles(usersA))
	fb := saveFingerprint(usersB, collectSaveFiles(usersB))
	if fa != fb {
		t.Errorf("empreintes après synchro : A=%q B=%q", fa, fb)
	}
}

func TestSyncApplyWithoutProfile(t *testing.T) {
	root := t.TempDir()
	usersA := filepath.Join(root, "A", "users")
	syncTestPopulate(t, usersA, "deck")
	var buf bytes.Buffer
	if _, err := writeSaveArchive(&buf, usersA, collectSaveFiles(usersA)); err != nil {
		t.Fatal(err)
	}
	archive := buf.Bytes()

	// B sans profil (seulement le compte Public) : origProfile sert de nom.
	usersB := filepath.Join(root, "B", "users")
	syncTestPut(t, filepath.Join(usersB, "Public", "Documents", "pub.sav"), "public", syncTestTime)
	if err := applySaveArchive(bytes.NewReader(archive), usersB, "deck"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(usersB, "deck", "Documents", "Game", "save1.sav")); err != nil {
		t.Errorf("origProfile deck : save1.sav attendu : %v", err)
	}

	// Sans origProfile : steamuser.
	usersC := filepath.Join(root, "C", "users")
	if err := applySaveArchive(bytes.NewReader(archive), usersC, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(usersC, syncDefaultProfile, "Documents", "Game", "save1.sav")); err != nil {
		t.Errorf("profil par défaut : save1.sav attendu : %v", err)
	}

	// origProfile système : refusé, rien écrit.
	usersD := filepath.Join(root, "D", "users")
	if err := applySaveArchive(bytes.NewReader(archive), usersD, "Public"); err == nil {
		t.Error("origProfile système : erreur attendue")
	}
	if _, err := os.Stat(usersD); !os.IsNotExist(err) {
		t.Error("origProfile refusé : users ne doit pas être créé")
	}
}

func TestSyncApplyRefusesMalicious(t *testing.T) {
	dirHdr := func(name string) *tar.Header {
		return &tar.Header{Typeflag: tar.TypeDir, Name: name, Mode: 0o755}
	}
	fileHdr := func(name string) *tar.Header {
		return &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: 4}
	}
	cases := map[string]*tar.Header{
		"chemin ..":               fileHdr("../x"),
		"token puis ..":           fileHdr("_profil_/../../x"),
		"absolu":                  fileHdr("/etc/x"),
		"lien symbolique":         {Typeflag: tar.TypeSymlink, Name: "_profil_/Documents/lien.sav", Linkname: "/etc/passwd"},
		"hors saveProfileDirs":    fileHdr("_profil_/Pictures/p.png"),
		"fichier hors profil":     fileHdr("x.sav"),
		"profil système":          fileHdr("Public/Documents/x.sav"),
		"cache":                   fileHdr("_profil_/Documents/Temp/x"),
		"dossier hors sauvegarde": dirHdr("_profil_/Pictures"),
	}
	for name, hdr := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			usersB := filepath.Join(root, "users")
			syncTestPopulate(t, usersB, "arcade")
			before := syncTestTree(t, root)
			err := applySaveArchive(syncTestArchive(t, hdr), usersB, "deck")
			if err == nil {
				t.Fatal("erreur attendue")
			}
			after := syncTestTree(t, root)
			if len(before) != len(after) {
				t.Errorf("arborescence modifiée : %d -> %d entrées", len(before), len(after))
			}
			for k, v := range before {
				if after[k] != v {
					t.Errorf("%s modifié", k)
				}
			}
		})
	}
}

func TestSyncDecide(t *testing.T) {
	cases := []struct {
		name      string
		lfp, lst  string
		lnew, lsz int64
		rfp, rst  string
		rnew, rsz int64
		want      syncAction
	}{
		{"identiques", "a", "a", 10, 1, "a", "a", 10, 1, syncUpToDate},
		{"vides", "", "", 0, 0, "", "", 0, 0, syncUpToDate},
		{"identiques, état absent", "a", "", 10, 1, "a", "", 10, 1, syncUpToDate},
		{"local vide", "", "", 0, 0, "b", "b", 5, 3, syncReceive},
		{"distant vide", "a", "a", 5, 3, "", "", 0, 0, syncSend},
		{"local vide, état ancien", "", "a", 0, 0, "b", "b", 5, 3, syncReceive},
		{"local changé seul", "a2", "a", 10, 1, "b", "b", 5, 9, syncSend},
		{"local changé, jamais synchro", "a", "", 10, 1, "b", "b", 5, 9, syncSend},
		{"distant changé seul", "a", "a", 10, 9, "b2", "b", 5, 1, syncReceive},
		{"deux inchangés, empreintes différentes", "a", "a", 10, 5, "b", "b", 5, 5, syncConflictSend},
		{"conflit, local plus récent, même taille", "a2", "a", 20, 5, "b2", "b", 10, 5, syncConflictSend},
		{"conflit, distant plus récent, même taille", "a2", "a", 10, 5, "b2", "b", 20, 5, syncConflictReceive},
		{"conflit, à égalité", "a2", "a", 10, 5, "b2", "b", 10, 5, syncConflictSend},
		{"conflit, état absent des deux", "a", "", 10, 5, "b", "", 20, 5, syncConflictReceive},
		{"conflit, local plus lourde mais plus ancienne", "a2", "a", 10, 300, "b2", "b", 20, 200, syncConflictSend},
		{"conflit, distant plus lourd mais plus ancien", "a2", "a", 20, 100, "b2", "b", 10, 300, syncConflictReceive},
		{"conflit, distant plus lourd et plus récent", "a2", "a", 10, 100, "b2", "b", 20, 300, syncConflictReceive},
		{"conflit, local plus lourd et plus récent", "a2", "a", 20, 300, "b2", "b", 10, 100, syncConflictSend},
	}
	for _, c := range cases {
		if got := decideSync(c.lfp, c.lst, c.lnew, c.lsz, c.rfp, c.rst, c.rnew, c.rsz); got != c.want {
			t.Errorf("%s : %v, attendu %v", c.name, got, c.want)
		}
	}
	labels := map[syncAction]string{
		syncUpToDate: "à jour", syncSend: "envoi", syncReceive: "réception",
		syncConflictSend: "conflit (envoi)", syncConflictReceive: "conflit (réception)",
	}
	for a, l := range labels {
		if a.String() != l {
			t.Errorf("String() = %q, attendu %q", a.String(), l)
		}
	}
}

// TestSyncSaveSize : somme des tailles du périmètre, 0 si vide.
func TestSyncSaveSize(t *testing.T) {
	if saveSize(nil) != 0 {
		t.Error("saveSize(vide) : attendu 0")
	}
	files := []saveFile{{Rel: "deck/a.sav", Size: 3}, {Rel: "deck/b.sav", Size: 4}}
	if got := saveSize(files); got != 7 {
		t.Errorf("saveSize = %d, attendu 7", got)
	}
}

func TestSyncState(t *testing.T) {
	root := t.TempDir()
	if st := loadSyncState(root, "steambox", "Game"); st != (syncState{}) {
		t.Errorf("absent : %+v, attendu zéro", st)
	}
	if err := saveSyncState(root, "steambox", "Game", "abc"); err != nil {
		t.Fatal(err)
	}
	if st := loadSyncState(root, "steambox", "Game"); st.Fingerprint != "abc" || st.At.IsZero() {
		t.Errorf("aller-retour : %+v", st)
	}
	if _, err := os.Stat(filepath.Join(root, "steambox", "Game.json")); err != nil {
		t.Errorf("fichier attendu : %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "steambox", "Game.json.tmp")); !os.IsNotExist(err) {
		t.Error("fichier .tmp resté après écriture")
	}
	if st := loadSyncState(root, "autre", "Game"); st != (syncState{}) {
		t.Errorf("autre machine : %+v, attendu zéro", st)
	}
	if err := saveSyncState(root, "../x", "Game", "abc"); err == nil {
		t.Error("identifiant de machine invalide : erreur attendue")
	}
	if st := loadSyncState(root, "../x", "Game"); st != (syncState{}) {
		t.Errorf("identifiant invalide en lecture : %+v", st)
	}
}

func TestSyncValidIDs(t *testing.T) {
	for _, id := range []string{"steambox", "AIBox-1", "deck_2", strings.Repeat("a", 64)} {
		if !validSyncID(id) {
			t.Errorf("validSyncID(%q) = false", id)
		}
	}
	for _, id := range []string{"", "../x", "a/b", ".sync", "a b", strings.Repeat("a", 65)} {
		if validSyncID(id) {
			t.Errorf("validSyncID(%q) = true", id)
		}
	}
	for _, g := range []string{"Game", "Jeu Deux"} {
		if !validSyncGame(g) {
			t.Errorf("validSyncGame(%q) = false", g)
		}
	}
	for _, g := range []string{"", "../x", ".sync", "a/b", "x\x00", "jeu.wsquashfs", "x.pc"} {
		if validSyncGame(g) {
			t.Errorf("validSyncGame(%q) = true", g)
		}
	}
}

func TestSyncUsersDir(t *testing.T) {
	oldSaves, oldCache := savesDir, cacheDir
	t.Cleanup(func() { savesDir, cacheDir = oldSaves, oldCache })
	savesDir = t.TempDir()
	cacheDir = t.TempDir()
	// Overlay : la couche avec des sauvegardes.
	if got, want := syncUsersDir("Game"), filepath.Join(savesDir, "Game", "drive_c", "users"); got != want {
		t.Errorf("sans rien : %q, attendu %q", got, want)
	}
	copyDir := filepath.Join(cacheDir, "wine", "Game")
	if err := os.MkdirAll(copyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copyDir, ".wsquashfs_cache"), []byte("sig"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := syncUsersDir("Game"), copyDir+"/drive_c/users"; got != want {
		t.Errorf("copie marquée : %q, attendu %q", got, want)
	}
	syncTestPut(t, filepath.Join(savesDir, "Game", "drive_c", "users", "deck", "Documents", "s.sav"), "x", syncTestTime)
	if got, want := syncUsersDir("Game"), filepath.Join(savesDir, "Game", "drive_c", "users"); got != want {
		t.Errorf("overlay avec sauvegardes : %q, attendu %q", got, want)
	}
}

// TestSyncEmptyExtraProfile : B a un profil vide en plus (arcade) ; les sauvegardes sont dans
// steamuser. Les empreintes doivent rester égales et les fichiers reçus dans steamuser.
func TestSyncEmptyExtraProfile(t *testing.T) {
	root := t.TempDir()
	usersA := filepath.Join(root, "A", "users")
	usersB := filepath.Join(root, "B", "users")
	syncTestPopulate(t, usersA, "deck")
	syncTestPopulate(t, usersB, "steamuser")
	syncTestPut(t, filepath.Join(usersB, "steamuser", "Documents", "Game", "only_b.sav"), "b", syncTestTime)
	if err := os.Remove(filepath.Join(usersB, "steamuser", "Documents", "Game", "save1.sav")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(usersB, "arcade"), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, err := writeSaveArchive(&buf, usersA, collectSaveFiles(usersA)); err != nil {
		t.Fatal(err)
	}
	if err := applySaveArchive(bytes.NewReader(buf.Bytes()), usersB, "deck"); err != nil {
		t.Fatalf("applySaveArchive : %v", err)
	}
	if _, err := os.Stat(filepath.Join(usersB, "steamuser", "Documents", "Game", "save1.sav")); err != nil {
		t.Errorf("save1.sav attendu dans steamuser : %v", err)
	}
	if _, err := os.Stat(filepath.Join(usersB, "steamuser", "Documents", "Game", "only_b.sav")); !os.IsNotExist(err) {
		t.Error("only_b.sav : attendu supprimé")
	}
	if entries, _ := os.ReadDir(filepath.Join(usersB, "arcade")); len(entries) != 0 {
		t.Errorf("profil vide arcade : %v, attendu vide", entries)
	}
	fa := saveFingerprint(usersA, collectSaveFiles(usersA))
	fb := saveFingerprint(usersB, collectSaveFiles(usersB))
	if fa == "" || fa != fb {
		t.Errorf("empreintes A=%q B=%q : attendues égales", fa, fb)
	}
}

// TestSyncRefusesSymlinkDest : users/arcade/Documents est un lien vers un dossier extérieur.
// La synchro est refusée, rien n'est écrit dehors ni supprimé dans B.
func TestSyncRefusesSymlinkDest(t *testing.T) {
	root := t.TempDir()
	usersA := filepath.Join(root, "A", "users")
	syncTestPopulate(t, usersA, "deck")
	var buf bytes.Buffer
	if _, err := writeSaveArchive(&buf, usersA, collectSaveFiles(usersA)); err != nil {
		t.Fatal(err)
	}

	bRoot := filepath.Join(root, "B")
	usersB := filepath.Join(bRoot, "users")
	syncTestPopulate(t, usersB, "arcade")
	syncTestPut(t, filepath.Join(usersB, "arcade", "Documents", "Game", "only_b.sav"), "b", syncTestTime)
	outside := filepath.Join(root, "outside")
	syncTestPut(t, filepath.Join(outside, "keep.txt"), "keep", syncTestTime)
	// Documents de B devient un lien vers le dossier extérieur.
	if err := os.RemoveAll(filepath.Join(usersB, "arcade", "Documents")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(usersB, "arcade", "Documents")); err != nil {
		t.Fatal(err)
	}

	beforeB := syncTestTree(t, bRoot)
	beforeOut := syncTestTree(t, outside)
	err := applySaveArchive(bytes.NewReader(buf.Bytes()), usersB, "deck")
	if err == nil || !strings.Contains(err.Error(), "est un lien") {
		t.Fatalf("erreur « est un lien » attendue, reçu %v", err)
	}
	afterOut := syncTestTree(t, outside)
	if len(beforeOut) != len(afterOut) || afterOut[filepath.Join("", "keep.txt")] != beforeOut["keep.txt"] {
		t.Errorf("dossier extérieur modifié : avant %v, après %v", beforeOut, afterOut)
	}
	afterB := syncTestTree(t, bRoot)
	for k, v := range beforeB {
		if afterB[k] != v {
			t.Errorf("B modifié en %s", k)
		}
	}
	if len(beforeB) != len(afterB) {
		t.Errorf("B : %d entrées avant, %d après", len(beforeB), len(afterB))
	}
}

// Mutations (vérifiées à la main, code remis ensuite) :
//   - sans le contrôle des ".." dans checkSyncEntry : TestSyncApplyRefusesMalicious échoue ;
//   - token ignoré à la lecture d'archive (localSyncProfile non utilisé) : TestSyncRoundTrip échoue ;
//   - saveFingerprint sans remplacement du profil : TestSyncFingerprint échoue.
