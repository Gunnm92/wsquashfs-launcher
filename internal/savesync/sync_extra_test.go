package savesync

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTree : crée les fichiers (chemin relatif → contenu) sous root, avec une date fixe par fichier.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	i := 0
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(1_700_000_000+int64(i)*60, 0)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		i++
	}
}

// plainArchive : archive d'un dossier simple, en mémoire.
func plainArchive(t *testing.T, root string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tg := Target{Dir: root, Plain: true}
	if _, err := tg.WriteArchive(&buf, tg.Files()); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestPlainDirRoundTrip : un dossier simple archivé puis appliqué ailleurs donne les mêmes fichiers
// (dates comprises) et la même empreinte ; un fichier présent seulement à l'arrivée disparaît.
func TestPlainDirRoundTrip(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "save")
	writeTree(t, src, map[string]string{"slot1.sav": "aaa", "profil/options.ini": "x=1", "a/b/c.dat": "deep"})
	writeTree(t, dst, map[string]string{"ancien.sav": "vieux"})
	data := plainArchive(t, src)
	if err := applyPlainArchive(bytes.NewReader(data), dst); err != nil {
		t.Fatal(err)
	}
	a, b := Target{Dir: src, Plain: true}, Target{Dir: dst, Plain: true}
	if fa, fb := a.Fingerprint(a.Files()), b.Fingerprint(b.Files()); fa == "" || fa != fb {
		t.Errorf("empreintes : %q et %q", fa, fb)
	}
	if _, err := os.Stat(filepath.Join(dst, "ancien.sav")); !os.IsNotExist(err) {
		t.Errorf("ancien.sav devait disparaître : %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "a", "b", "c.dat")); string(got) != "deep" {
		t.Errorf("a/b/c.dat : %q", got)
	}
}

// tarGz : archive faite à la main (entrées piégées).
func tarGz(t *testing.T, entries []tar.Header, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		h := h
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(body))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// TestPlainArchiveRefusesTraps : chemin qui remonte, chemin absolu, lien, archive vide : refusés, et
// le dossier d'arrivée n'est pas modifié.
func TestPlainArchiveRefusesTraps(t *testing.T) {
	cases := map[string][]tar.Header{
		"remonte": {{Typeflag: tar.TypeReg, Name: "../evade.sav", Mode: 0o644}},
		"absolu":  {{Typeflag: tar.TypeReg, Name: "/etc/evade", Mode: 0o644}},
		"lien":    {{Typeflag: tar.TypeSymlink, Name: "lien", Linkname: "/etc/passwd"}},
		"vide":    {{Typeflag: tar.TypeDir, Name: "dossier/", Mode: 0o755}},
	}
	for name, entries := range cases {
		dst := t.TempDir()
		writeTree(t, dst, map[string]string{"garde.sav": "intact"})
		if err := applyPlainArchive(bytes.NewReader(tarGz(t, entries, "x")), dst); err == nil {
			t.Errorf("%s : archive acceptée", name)
		}
		if got, _ := os.ReadFile(filepath.Join(dst, "garde.sav")); string(got) != "intact" {
			t.Errorf("%s : dossier modifié (%q)", name, got)
		}
	}
}

// TestSyncDirPathChecks : chemin relatif, hors HOME ou HOME lui-même : refusés ; sous HOME, même
// inexistant : accepté. Noms : lettres, chiffres, espace, _ et -.
func TestSyncDirPathChecks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	real, _ := filepath.EvalSymlinks(home)
	for _, p := range []string{"", "~", ".", "../ailleurs", "/etc", home, filepath.Join(home, "..", "ailleurs")} {
		if _, err := checkSyncDirPath(p); err == nil {
			t.Errorf("%q accepté", p)
		}
	}
	want := filepath.Join(real, ".local", "share", "hookshot", "save")
	// À partir de HOME (forme attendue), avec ~/, ou absolu sous HOME : même dossier
	for _, p := range []string{".local/share/hookshot/save", "~/.local/share/hookshot/save",
		filepath.Join(home, ".local", "share", "hookshot", "save")} {
		if got, err := checkSyncDirPath(p); err != nil || got != want {
			t.Errorf("%q : %q, %v", p, got, err)
		}
	}
	if got := HomeRel(want); got != ".local/share/hookshot/save" {
		t.Errorf("affichage : %q", got)
	}
	for _, n := range []string{"", "../x", "a/b", ".cache", strings.Repeat("a", 49)} {
		if checkSyncDirName(n) == nil {
			t.Errorf("nom %q accepté", n)
		}
	}
	if checkSyncDirName("hookshot save_2") != nil {
		t.Error("nom valide refusé")
	}
}

// TestSyncDirsDeclareAndForget : déclarer remplace un même nom ; retirer ne touche pas aux fichiers.
func TestSyncDirsDeclareAndForget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg, err := AddDir(Config{}, "hookshot", filepath.Join(home, "a"))
	if err == nil {
		cfg, err = AddDir(cfg, "hookshot", filepath.Join(home, "b"))
	}
	if err != nil || len(cfg.Dirs) != 1 || filepath.Base(cfg.Dirs[0].Path) != "b" {
		t.Fatalf("déclaration : %+v, %v", cfg.Dirs, err)
	}
	cfg, found := RemoveDir(cfg, "hookshot")
	if !found || len(cfg.Dirs) != 0 {
		t.Errorf("retrait : %+v, %v", cfg.Dirs, found)
	}
	if _, found := RemoveDir(cfg, "inconnu"); found {
		t.Error("nom inconnu retiré")
	}
}

// edenFixture : Eden dans un HOME temporaire : deux profils (le second a plus de jeux), un nom de jeu
// dans le cache de la liste. Retourne le dossier du profil retenu.
func edenFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("WSQUASHFS_EDEN_DIR", "")
	root := filepath.Join(home, ".local", "share", "eden", "nand", "user", "save", "0000000000000000")
	small := filepath.Join(root, "00000000000000000000000000000001")
	big := filepath.Join(root, "0000000000000000000000000000000A")
	writeTree(t, small, map[string]string{"01007EF00011E000/save.bin": "petit"})
	writeTree(t, big, map[string]string{
		"0100F2C0115B6000/progress.dat": "zelda",
		"01006A800016E000/slot0":        "mario",
	})
	if err := os.MkdirAll(filepath.Join(big, "01000000DEADBEEF"), 0o755); err != nil { // jeu sans sauvegarde
		t.Fatal(err)
	}
	romNames = nil // cache des noms de ROM (30 s) : celui d'un autre test ne vaut pas ici
	t.Cleanup(func() { romNames = nil })
	cache := filepath.Join(home, ".cache", "eden", "game_list")
	writeTree(t, cache, map[string]string{"0100F2C0115B6000.appname.txt": "The Legend of Zelda: Tears of the Kingdom\n"})
	return big
}

// TestEdenTargets : profil aux plus de jeux retenu ; jeux avec sauvegardes listés en switch:<ID> ;
// nom lu dans le cache d'Eden, sinon l'identifiant ; un jeu sans sauvegarde n'est pas listé.
func TestEdenTargets(t *testing.T) {
	user := edenFixture(t)
	s := testSyncer(t)
	if got := edenSaveUser(); got != user {
		t.Fatalf("profil : %q, attendu %q", got, user)
	}
	names := s.extraSyncNames()
	want := []string{"switch:01006A800016E000", "switch:0100F2C0115B6000"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("entrées : %v, attendu %v", names, want)
	}
	tg, err := s.TargetOf("switch:0100F2C0115B6000")
	if err != nil || tg.Dir != filepath.Join(user, "0100F2C0115B6000") || !tg.Plain ||
		tg.Label != "Switch · The Legend of Zelda: Tears of the Kingdom" {
		t.Errorf("cible : %+v, %v", tg, err)
	}
	if tg, _ := s.TargetOf("switch:01006A800016E000"); tg.Label != "Switch · 01006A800016E000" {
		t.Errorf("nom inconnu : %q", tg.Label)
	}
	// Un jeu Switch absent ici peut être reçu : la cible existe dans le profil retenu.
	if tg, err := s.TargetOf("switch:0100000000010000"); err != nil || tg.Dir != filepath.Join(user, "0100000000010000") {
		t.Errorf("jeu absent : %+v, %v", tg, err)
	}
}

// TestSyncTargetErrors : noms invalides, dossier non déclaré, Eden absent.
func TestSyncTargetErrors(t *testing.T) {
	s := testSyncer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("WSQUASHFS_EDEN_DIR", "")
	for _, n := range []string{"switch:0100abc", "switch:../x", "dossier:../x", "dossier:", ".cache"} {
		if _, err := s.TargetOf(n); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q : %v, attendu nom invalide", n, err)
		}
	}
	for _, n := range []string{"dossier:hookshot", "switch:0100F2C0115B6000"} {
		if _, err := s.TargetOf(n); !errors.Is(err, ErrAbsent) {
			t.Errorf("%q : %v, attendu absent", n, err)
		}
	}
}

// TestSwitchNamesFromRomFiles : sans cache de noms d'Eden (cas de la SteamBox), le nom vient du
// fichier de la ROM, dans un dossier de jeux d'Eden (qt-config.ini) ou dans switch/ des roms de
// l'agent ; une ROM de mise à jour (…800) nomme son jeu de base (…000).
func TestSwitchNamesFromRomFiles(t *testing.T) {
	user := edenFixture(t)
	s := testSyncer(t)
	home := os.Getenv("HOME")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	_ = os.RemoveAll(filepath.Join(home, ".cache", "eden"))
	writeTree(t, filepath.Join(user, "01006F8002326000"), map[string]string{"main": "animal"})
	writeTree(t, filepath.Join(user, "0100F9F00C696000"), map[string]string{"main": "crash"})
	edenRoms := filepath.Join(home, "jeux", "switch")
	writeTree(t, edenRoms, map[string]string{
		"Animal Crossing New Horizons [01006F8002326000][v0].nsp":        "",
		"Crash Team Racing Nitro-Fueled [0100F9F00C696800][v196608].nsp": "",
		"Everybody 1-2-Switch (v1.0.1).xci":                              "", // sans TitleID : ignoré
	})
	writeTree(t, filepath.Join(home, ".config", "eden"), map[string]string{
		"qt-config.ini": "[UI]\nPaths\\gamedirs\\1\\path=SDMC\nPaths\\gamedirs\\4\\path=" + edenRoms + "\n",
	})
	agentRoms := filepath.Join(home, "roms")
	writeTree(t, filepath.Join(agentRoms, "switch"), map[string]string{
		"Zelda TOTK [0100F2C0115B6000][v0].xci": "",
		// Fichier renommé sans TitleID ; la fiche du gamelist garde l'ancien chemin et le nom scrapé
		"Fire Emblem Fortunes Weave (v1.1.1).nup": "",
		"gamelist.xml": `<?xml version="1.0"?><gameList><game><path>./Fire Emblem Fortunes Weave [04007EF01F8AE000].nup</path>` +
			`<name>Fire Emblem &amp; Fortune's Weave</name></game><game><path>./sans-id.nsp</path><name>Sans ID</name></game></gameList>`,
	})
	s.env.AgentRoms = func() []string { return []string{agentRoms} }
	romNames = nil
	for id, want := range map[string]string{
		"01006F8002326000": "Animal Crossing New Horizons",
		"0100F9F00C696000": "Crash Team Racing Nitro-Fueled",
		"0100F2C0115B6000": "Zelda TOTK",
		"04007EF01F8AE000": "Fire Emblem & Fortune's Weave",
		"01006A800016E000": "",
	} {
		if got := s.edenTitleName(id); got != want {
			t.Errorf("%s : %q, attendu %q", id, got, want)
		}
	}
	if tg, _ := s.TargetOf("switch:01006F8002326000"); tg.Label != "Switch · Animal Crossing New Horizons" {
		t.Errorf("libellé : %q", tg.Label)
	}
}
