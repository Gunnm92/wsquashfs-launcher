package agent

// Tests de l'agent : authentification, chemins autorisés, certificat et code
// d'association. Pas de réseau réel : les routes sont appelées en mémoire.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"wsquashfs-launcher/internal/savesync"
)

func TestAgentRequiresToken(t *testing.T) {
	_, h, _ := agentFixture(t)
	if rec := do(h, "GET", "/v1/info", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("sans jeton : code %d, attendu 401", rec.Code)
	}
	if rec := do(h, "GET", "/v1/info", "mauvais", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("mauvais jeton : code %d, attendu 401", rec.Code)
	}
	if rec := do(h, "GET", "/v1/info", "secret-token", ""); rec.Code != http.StatusOK {
		t.Errorf("bon jeton : code %d, attendu 200", rec.Code)
	}
}

func TestAgentDoctorPassesLauncherJSON(t *testing.T) {
	_, h, _ := agentFixture(t)
	rec := do(h, "GET", "/v1/doctor", "secret-token", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"verdict":"ok"`) {
		t.Errorf("doctor : code %d, corps %q", rec.Code, rec.Body.String())
	}
}

func TestResolveImageRefusesEscapes(t *testing.T) {
	s, _, roms := agentFixture(t)
	outside := filepath.Join(filepath.Dir(roms), "outside.wsquashfs")
	if err := os.WriteFile(outside, []byte("hsqs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(roms, "win", "lien.wsquashfs")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]int{
		"/etc/passwd.wsquashfs": http.StatusBadRequest,
		"../outside.wsquashfs":  http.StatusBadRequest,
		"win/../../x.wsquashfs": http.StatusBadRequest,
		"win/Jeu.txt":           http.StatusBadRequest,
		"win/lien.wsquashfs":    http.StatusNotFound, // lien qui sort de roms/
		"win/absent.wsquashfs":  http.StatusNotFound,
		"win/Jeu.wsquashfs":     0,
	}
	for rel, want := range cases {
		_, code, _ := s.resolveImage(rel)
		if code != want {
			t.Errorf("resolveImage(%q) = %d, attendu %d", rel, code, want)
		}
	}
}

// TestMaintenanceOnlyListedActions : seules les actions de la table sont acceptées, et la règle sur
// le jeu (rel) est respectée : interdit pour list, obligatoire pour save.
func TestMaintenanceOnlyListedActions(t *testing.T) {
	_, h, _ := agentFixture(t)
	for _, body := range []string{
		`{"action":"clear"}`, `{"action":"erase"}`, `{"action":"pack"}`, `{"action":"install"}`,
		`{"action":"trial"}`, `{"action":"agent"}`, `{"action":"update"}`, `{"action":"saves-all"}`,
		`{"action":"list","rel":"win/Jeu.wsquashfs"}`,
		`{"action":"umount","rel":"win/Jeu.wsquashfs"}`,
		`{"action":"save"}`,
		`{"action":"saves","rel":"win/Jeu.wsquashfs"}`,
		`{"action":"sync-status","rel":"win/Jeu.wsquashfs"}`,
		`{"action":"save","rel":"../Jeu.wsquashfs"}`,
	} {
		if rec := do(h, "POST", "/v1/maintenance", "secret-token", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s accepté (code %d) : doit être refusé", body, rec.Code)
		}
	}
	if rec := do(h, "POST", "/v1/maintenance", "secret-token", `{"action":"list"}`); rec.Code != http.StatusOK {
		t.Errorf("--list refusé (code %d)", rec.Code)
	}
}

// TestMaintenanceArgs : arguments réellement passés au lanceur pour chaque action de la table. Le
// faux lanceur affiche chaque argument entre chevrons.
func TestMaintenanceArgs(t *testing.T) {
	isolateState(t)
	_, h, roms := agentFixture(t)
	fake := filepath.Join(t.TempDir(), "fake-launcher")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '<%s>' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	testExe = fake
	image, err := filepath.EvalSymlinks(filepath.Join(roms, "win", "Jeu.wsquashfs"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ body, want string }{
		{`{"action":"save","rel":"win/Jeu.wsquashfs"}`, "<--save><" + image + ">"},
		{`{"action":"saves"}`, "<--saves>"},
		{`{"action":"sync"}`, "<--sync>"},
		{`{"action":"sync","rel":"win/Jeu.wsquashfs"}`, "<--sync><" + image + ">"},
		{`{"action":"sync-status"}`, "<--sync><status>"},
		{`{"action":"list"}`, "<--list>"},
		{`{"action":"umount"}`, "<--umount>"},
		{`{"action":"clean"}`, "<--clean>"},
	}
	for _, c := range cases {
		rec := do(h, "POST", "/v1/maintenance", "secret-token", c.body)
		var got struct {
			Output string `json:"output"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Errorf("%s : code %d, réponse %s", c.body, rec.Code, rec.Body.String())
			continue
		}
		if got.Output != c.want {
			t.Errorf("%s : arguments %q, attendus %q", c.body, got.Output, c.want)
		}
	}
	// save sans jeu : 400 avec le motif.
	rec := do(h, "POST", "/v1/maintenance", "secret-token", `{"action":"save"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "--save attend un jeu") {
		t.Errorf("save sans jeu : code %d, %s", rec.Code, rec.Body.String())
	}
	// Version de l'API : 4 depuis la table (save, saves, sync, sync-status, taille et OS).
	rec = do(h, "GET", "/v1/info", "secret-token", "")
	var info struct {
		Version string `json:"version"`
		OS      string `json:"os"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil || info.Version != agentVersion || info.OS == "" {
		t.Errorf("/v1/info : version et os attendus, %s (%v)", rec.Body.String(), err)
	}
}

func TestTrialValidatesDuration(t *testing.T) {
	_, h, _ := agentFixture(t)
	for _, body := range []string{`{"rel":"win/Jeu.wsquashfs","duration":5}`, `{"rel":"win/Jeu.wsquashfs","duration":601}`} {
		if rec := do(h, "POST", "/v1/trial", "secret-token", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s : code %d, attendu 400", body, rec.Code)
		}
	}
}

func TestRunnerRoutesOnlyInstalledRunners(t *testing.T) {
	_, h, _ := agentFixture(t)
	if rec := do(h, "GET", "/v1/runner/list?path=/etc", "secret-token", ""); rec.Code != http.StatusForbidden {
		t.Errorf("/etc lu comme runner (code %d)", rec.Code)
	}
	if rec := do(h, "GET", "/v1/runner/list?path=relatif", "secret-token", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("chemin relatif accepté (code %d)", rec.Code)
	}
}

func TestPairCodeMatchesCertificate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := ensureAgentCert(); err != nil {
		t.Fatal(err)
	}
	fp, err := certFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	// L'empreinte est celle du DER du certificat (SHA-256, hexadécimal).
	data, _ := os.ReadFile(filepath.Join(agentDir(), "cert.pem"))
	block, _ := pem.Decode(data)
	sum := sha256.Sum256(block.Bytes)
	if want := strings.ToUpper(hex.EncodeToString(sum[:])); fp != want {
		t.Errorf("empreinte %s, attendu %s", fp, want)
	}
	code := pairCode("jeton_TEST-123", "192.168.1.20", 8790, fp)
	re := regexp.MustCompile(`^wsq://([A-Za-z0-9_-]+)@([^:]+):(\d+)#([0-9A-F]{64})$`)
	m := re.FindStringSubmatch(code)
	if m == nil {
		t.Fatalf("code d'association mal formé : %q", code)
	}
	if m[1] != "jeton_TEST-123" || m[2] != "192.168.1.20" || m[3] != "8790" || m[4] != fp {
		t.Errorf("code d'association incorrect : %q", code)
	}
	// Le code produit par --pair est lu par --sync wsq:// (parsePairCode).
	if p, err := savesync.ParsePairCode(code); err != nil || p.Token != "jeton_TEST-123" {
		t.Errorf("code relu : %+v, %v", p, err)
	}
}

func TestCertificateIsEcdsaTenYears(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := ensureAgentCert(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(agentDir(), "cert.pem"))
	if !strings.Contains(string(data), "BEGIN CERTIFICATE") {
		t.Fatal("certificat PEM absent")
	}
	keyInfo, err := os.Stat(filepath.Join(agentDir(), "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Errorf("clé en %v, attendu 600", keyInfo.Mode().Perm())
	}
}

func TestNewTokenIsRandom(t *testing.T) {
	a, _ := newToken()
	b, _ := newToken()
	if a == b || len(a) < 40 {
		t.Errorf("jetons identiques ou trop courts : %q %q", a, b)
	}
}

// TestMaintenanceForcesColor : la sortie de maintenance est colorée pour le
// manager (CLICOLOR_FORCE=1 transmis au lanceur appelé).
func TestMaintenanceForcesColor(t *testing.T) {
	isolateState(t)
	_, h, _ := agentFixture(t)
	fake := filepath.Join(t.TempDir(), "fake-launcher")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"force=$CLICOLOR_FORCE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	testExe = fake
	rec := do(h, "POST", "/v1/maintenance", "secret-token", `{"action":"list"}`)
	if !strings.Contains(rec.Body.String(), "force=1") {
		t.Errorf("couleurs non forcées pour la maintenance : %s", rec.Body.String())
	}
}

// TestAgentStop : --agent stop arrête le processus inscrit dans le fichier de
// PID s'il est un agent, et ignore un PID périmé (sans tuer un autre processus).
func TestAgentStop(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	if err := os.MkdirAll(agentDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// Faux agent : sa ligne de commande contient « --agent » (argument $0 de
	// sh). Piège : avec « sh -c "sleep 30" », dash exécute directement sleep
	// (exec du dernier ordre), « --agent » disparaît de /proc/<pid>/cmdline et
	// cmdAgentStop croit le PID périmé. « ; : » interdit ce remplacement : sh
	// reste le processus inscrit et meurt sur SIGTERM. Son enfant « sleep »
	// survit alors à sh : il est dans un groupe de processus propre, tué
	// entièrement au nettoyage.
	fake := exec.Command("sh", "-c", "sleep 30; :", "--agent")
	fake.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := fake.Start(); err != nil {
		t.Fatal(err)
	}
	// Course : Start rend la main dès l'execve réussi, mais le noyau n'a pas
	// encore renseigné les arguments du nouveau processus : /proc/<pid>/cmdline
	// est vide un bref instant. On attend qu'il contienne « --agent ».
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", fake.Process.Pid)); strings.Contains(string(data), "--agent\x00") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("faux agent : « --agent » absent de sa ligne de commande")
		}
	}
	// Récupéré dès sa fin (comme le ferait le système pour un agent lancé
	// hors du shell) : sinon il reste zombie et « kill 0 » le voit encore.
	reaped := make(chan struct{})
	go func() { fake.Wait(); close(reaped) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-fake.Process.Pid, syscall.SIGKILL) // groupe : sh et sleep
		<-reaped
	})
	if err := os.WriteFile(agentPidFile(), []byte(strconv.Itoa(fake.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := capteUI(t)
	if code := cmdAgentStop(); code != 0 {
		t.Fatalf("code %d", code)
	}
	<-reaped
	if !strings.Contains(out.String(), "Agent arrêté") {
		t.Errorf("sortie : %q", out.String())
	}
	if pathExistsFile(agentPidFile()) {
		t.Error("fichier de PID gardé après l'arrêt")
	}

	// PID périmé : un processus quelconque (ici le test lui-même) n'est pas touché.
	if err := os.WriteFile(agentPidFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdAgentStop(); code != 0 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(out.String(), "périmé") || pathExistsFile(agentPidFile()) {
		t.Errorf("PID périmé mal traité : %q", out.String())
	}
}

func pathExistsFile(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// TestMaintenanceReturnsExitCode : la réponse porte le code de sortie du lanceur (0 ou 3), comme le
// mode local ; wsquashfs-manager s'en sert pour signaler un échec.
func TestMaintenanceReturnsExitCode(t *testing.T) {
	isolateState(t)
	_, h, _ := agentFixture(t)
	fake := filepath.Join(t.TempDir(), "fake-launcher")
	for _, c := range []struct {
		script string
		code   int
	}{{"echo sortie\nexit 3", 3}, {"echo sortie\nexit 0", 0}} {
		if err := os.WriteFile(fake, []byte("#!/bin/sh\n"+c.script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		testExe = fake
		rec := do(h, "POST", "/v1/maintenance", "secret-token", `{"action":"list"}`)
		var got struct {
			Code   *int   `json:"code"`
			Output string `json:"output"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
			t.Fatalf("exit %d : code HTTP %d, %s", c.code, rec.Code, rec.Body.String())
		}
		if got.Code == nil || *got.Code != c.code {
			t.Errorf("exit %d : code %v, attendu %d", c.code, got.Code, c.code)
		}
		if !strings.Contains(got.Output, "sortie") {
			t.Errorf("sortie absente : %q", got.Output)
		}
	}
}

// TestOSID : identifiant de distribution tiré d'os-release ; "linux" sans fichier ni ID.
func TestOSID(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := []struct {
		name, path, want string
	}{
		{"steamos", write("steamos", "NAME=\"SteamOS\"\nID=steamos\nVERSION_ID=3.5\n"), "steamos"},
		{"batocera, guillemets et majuscules", write("batocera", "ID_LIKE=linux\nID=\"Batocera\"\n"), "batocera"},
		{"absent", filepath.Join(dir, "absent"), "linux"},
		{"sans ID", write("sans", "NAME=Inconnu\nID_LIKE=debian\n"), "linux"},
		{"ID vide", write("vide", "ID=\"\"\n"), "linux"},
	}
	for _, c := range cases {
		if got := osID(c.path); got != c.want {
			t.Errorf("%s : osID = %q, attendu %q", c.name, got, c.want)
		}
	}
}

// ── Association : POST /v1/pair, jetons par machine ────────────────────

// machineNamed : la machine d'agent.json de cet identifiant, si elle y est.
func machineNamed(id string) (Machine, bool) {
	for _, m := range loadConfig().Machines {
		if m.ID == id {
			return m, true
		}
	}
	return Machine{}, false
}

// TestPairExchange : le code s'échange contre un jeton durable ; ce jeton ouvre les routes ; agent.json
// ne garde que son SHA-256 ; last-pair.json note l'association ; le code est retiré.
func TestPairExchange(t *testing.T) {
	_, h, _ := agentFixture(t)
	const secret = "secret-echange-01"
	if err := beginPairing(secret, time.Minute); err != nil {
		t.Fatal(err)
	}
	token, host, version := pairReplyOf(t, postPair(h, secret, "aaaa-machine-01", `{"name":"  Steam\u0001Box  "}`))
	if token == "" || token == secret || host == "" || version != agentVersion {
		t.Fatalf("réponse : jeton %q, hôte %q, version %q", token, host, version)
	}
	if rec := do(h, "GET", "/v1/info", token, ""); rec.Code != http.StatusOK {
		t.Errorf("jeton durable refusé : code %d", rec.Code)
	}
	m, ok := machineNamed("aaaa-machine-01")
	if !ok || m.Name != "SteamBox" || m.Paired.IsZero() {
		t.Fatalf("machine enregistrée : %+v (%v)", m, ok)
	}
	sum := sha256.Sum256([]byte(token))
	if m.TokenSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("token_sha256 %q, attendu le SHA-256 du jeton", m.TokenSHA256)
	}
	if _, ok := readPairOffer(); ok {
		t.Error("code d'association non retiré après usage")
	}
	last, ok := readLastPair()
	if !ok || last.ID != "aaaa-machine-01" || last.Name != "SteamBox" || last.Addr != "192.0.2.1" {
		t.Errorf("last-pair.json : %+v (%v)", last, ok)
	}
	// La machine déjà associée avant garde son accès.
	if rec := do(h, "GET", "/v1/info", "secret-token", ""); rec.Code != http.StatusOK {
		t.Errorf("machine existante refusée après une association : code %d", rec.Code)
	}
}

// TestPairCodeUsedOnce : un code réutilisé est refusé (401), sans nouvelle machine.
func TestPairCodeUsedOnce(t *testing.T) {
	_, h, _ := agentFixture(t)
	const secret = "secret-unique-02"
	if err := beginPairing(secret, time.Minute); err != nil {
		t.Fatal(err)
	}
	pairReplyOf(t, postPair(h, secret, "bbbb-machine-02", `{}`))
	rec := postPair(h, secret, "cccc-machine-03", `{}`)
	if rec.Code != http.StatusUnauthorized || detailOf(t, rec) != pairDetail {
		t.Errorf("code réutilisé : code %d, %s", rec.Code, rec.Body.String())
	}
	if _, ok := machineNamed("cccc-machine-03"); ok {
		t.Error("machine associée avec un code déjà utilisé")
	}
}

// TestPairCodeExpired : un code expiré est refusé et retiré, sans nouvelle machine.
func TestPairCodeExpired(t *testing.T) {
	_, h, _ := agentFixture(t)
	const secret = "secret-expire-03"
	if err := beginPairing(secret, -time.Second); err != nil {
		t.Fatal(err)
	}
	rec := postPair(h, secret, "dddd-machine-04", `{}`)
	if rec.Code != http.StatusUnauthorized || detailOf(t, rec) != pairDetail {
		t.Errorf("code expiré : code %d, %s", rec.Code, rec.Body.String())
	}
	if pathExistsFile(pairPath()) {
		t.Error("pair.json expiré gardé")
	}
	if _, ok := machineNamed("dddd-machine-04"); ok {
		t.Error("machine associée avec un code expiré")
	}
}

// TestPairWrongSecretKeepsCode : un mauvais secret est refusé (401) sans consommer le code : le bon
// secret s'associe ensuite.
func TestPairWrongSecretKeepsCode(t *testing.T) {
	_, h, _ := agentFixture(t)
	const secret = "secret-bon-04"
	if err := beginPairing(secret, time.Minute); err != nil {
		t.Fatal(err)
	}
	rec := postPair(h, "mauvais-secret", "eeee-machine-05", `{}`)
	if rec.Code != http.StatusUnauthorized || detailOf(t, rec) != pairDetail {
		t.Errorf("mauvais secret : code %d, %s", rec.Code, rec.Body.String())
	}
	if _, ok := readPairOffer(); !ok {
		t.Fatal("mauvais secret : code consommé")
	}
	pairReplyOf(t, postPair(h, secret, "eeee-machine-05", `{}`))
}

// TestPairNeedsMachineAndSmallBody : sans identifiant de machine valide (400) ou avec un corps trop
// grand (413), le code n'est pas consommé.
func TestPairNeedsMachineAndSmallBody(t *testing.T) {
	_, h, _ := agentFixture(t)
	const secret = "secret-entete-05"
	if err := beginPairing(secret, time.Minute); err != nil {
		t.Fatal(err)
	}
	if rec := postPair(h, secret, "", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("sans X-Wsq-Machine : code %d, attendu 400", rec.Code)
	}
	if rec := postPair(h, secret, "pas valide/x", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("identifiant invalide : code %d, attendu 400", rec.Code)
	}
	big := `{"name":"` + strings.Repeat("a", 5000) + `"}`
	if rec := postPair(h, secret, "ffff-machine-06", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("corps de 5 Ko : code %d, attendu 413", rec.Code)
	}
	if _, ok := readPairOffer(); !ok {
		t.Fatal("requête refusée : code consommé")
	}
	pairReplyOf(t, postPair(h, secret, "ffff-machine-06", `{}`))
}

// TestPairEmptyNameIsAddress : sans nom (ou nom vide après nettoyage), la machine porte l'adresse du client.
func TestPairEmptyNameIsAddress(t *testing.T) {
	_, h, _ := agentFixture(t)
	if err := beginPairing("secret-vide-06", time.Minute); err != nil {
		t.Fatal(err)
	}
	pairReplyOf(t, postPair(h, "secret-vide-06", "gggg-machine-07", `{"name":" \u0002 "}`))
	if m, ok := machineNamed("gggg-machine-07"); !ok || m.Name != "192.0.2.1" {
		t.Errorf("nom vide : %+v (%v), attendu l'adresse du client", m, ok)
	}
}

// TestCleanMachineName : espaces de bord et caractères de contrôle retirés, 64 caractères au plus.
func TestCleanMachineName(t *testing.T) {
	cases := map[string]string{
		" \x07 Salon PC \n":     "Salon PC",
		"\x00\x01":              "",
		strings.Repeat("é", 70): strings.Repeat("é", 64),
		"  Deck\tOLED  ":        "DeckOLED",
	}
	for in, want := range cases {
		if got := cleanMachineName(in); got != want {
			t.Errorf("cleanMachineName(%q) = %q, attendu %q", in, got, want)
		}
	}
}

// TestAgentJSONNeverHoldsPlainToken : après une association, agent.json ne contient ni le jeton durable
// en clair, ni le secret du code ; seulement le SHA-256 du jeton.
func TestAgentJSONNeverHoldsPlainToken(t *testing.T) {
	_, h, _ := agentFixture(t)
	const secret = "secret-clair-07"
	if err := beginPairing(secret, time.Minute); err != nil {
		t.Fatal(err)
	}
	token, _, _ := pairReplyOf(t, postPair(h, secret, "hhhh-machine-08", `{"name":"Deck"}`))
	data, err := os.ReadFile(filepath.Join(agentDir(), "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, clair := range []string{token, secret} {
		if strings.Contains(string(data), clair) {
			t.Errorf("agent.json contient un secret en clair : %s", data)
		}
	}
	if strings.Contains(string(data), `"token"`) {
		t.Errorf("champ token présent dans agent.json : %s", data)
	}
	sum := sha256.Sum256([]byte(token))
	if !strings.Contains(string(data), hex.EncodeToString(sum[:])) {
		t.Errorf("SHA-256 du jeton absent de agent.json : %s", data)
	}
}

// TestLegacyTokenFieldIgnored : un agent.json de l'ancien format (jeton en clair) se lit sans erreur ;
// le jeton ne donne plus accès, et le champ disparaît à la prochaine écriture.
func TestLegacyTokenFieldIgnored(t *testing.T) {
	agentEnv(t)
	if err := os.MkdirAll(agentDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"port":8790,"token":"ancien-jeton","roms":[],"session_process":"emulationstation"}`
	if err := os.WriteFile(filepath.Join(agentDir(), "agent.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := loadConfig()
	if cfg.Port != 8790 || len(cfg.Machines) != 0 {
		t.Fatalf("ancien agent.json : %+v", cfg)
	}
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(agentDir(), "agent.json"))
	if strings.Contains(string(data), "token") || strings.Contains(string(data), "ancien-jeton") {
		t.Errorf("champ token gardé à l'écriture : %s", data)
	}
	s := testServer().newService(cfg, false)
	if rec := do(s.httpServer().Handler, "GET", "/v1/info", "ancien-jeton", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("ancien jeton accepté : code %d", rec.Code)
	}
}

// TestMachineTokenRefusedAfterForget : --pair forget retire la machine (nom sans casse, ou identifiant) ;
// son jeton est refusé tout de suite. Machine inconnue : code 1.
func TestMachineTokenRefusedAfterForget(t *testing.T) {
	_, h, _ := agentFixture(t)
	out, errs := capteUI(t)
	enrollMachine(t, "autre-machine-02", "Autre", "jeton-autre")
	if rec := do(h, "GET", "/v1/info", "secret-token", ""); rec.Code != http.StatusOK {
		t.Fatalf("avant forget : code %d", rec.Code)
	}
	if code := cmdPairForget("POSTE-TEST"); code != 0 {
		t.Fatalf("forget par nom : code %d, %s", code, errs.String())
	}
	if rec := do(h, "GET", "/v1/info", "secret-token", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("jeton d'une machine retirée accepté : code %d", rec.Code)
	}
	if code := cmdPairForget("autre-machine-02"); code != 0 {
		t.Fatalf("forget par identifiant : code %d", code)
	}
	if rec := do(h, "GET", "/v1/info", "jeton-autre", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("jeton de la seconde machine retirée accepté : code %d", rec.Code)
	}
	if !strings.Contains(out.String(), "retirée") {
		t.Errorf("sortie : %q", out.String())
	}
	if code := cmdPairForget("inconnue"); code != 1 || !strings.Contains(errs.String(), "aucune machine associée") {
		t.Errorf("machine inconnue : code %d, erreur %q", code, errs.String())
	}
}

// TestMachineTokenRefusedAfterReset : --pair reset (pairRevokeAll) retire toutes les machines : aucun
// de leurs jetons ne passe, et l'agent sans machine refuse tout sauf POST /v1/pair.
func TestMachineTokenRefusedAfterReset(t *testing.T) {
	_, h, _ := agentFixture(t)
	capteUI(t)
	enrollMachine(t, "autre-machine-02", "Autre", "jeton-autre")
	for _, tok := range []string{"secret-token", "jeton-autre"} {
		if rec := do(h, "GET", "/v1/info", tok, ""); rec.Code != http.StatusOK {
			t.Fatalf("avant reset : code %d", rec.Code)
		}
	}
	names, err := pairRevokeAll()
	if err != nil || len(names) != 2 {
		t.Fatalf("reset : noms %v, %v", names, err)
	}
	for _, tok := range []string{"secret-token", "jeton-autre"} {
		if rec := do(h, "GET", "/v1/info", tok, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("jeton après reset accepté : code %d", rec.Code)
		}
	}
	if rec := postPair(h, "secret-sans-code", "iiii-machine-09", `{}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/pair sans code en attente : code %d", rec.Code)
	}
}

// TestMachineIDMustMatchToken : un en-tête X-Wsq-Machine qui n'est pas l'identifiant de la machine du
// jeton est refusé ; l'identifiant juste, ou l'absence d'en-tête, passent.
func TestMachineIDMustMatchToken(t *testing.T) {
	_, h, _ := agentFixture(t)
	enrollMachine(t, "autre-machine-02", "Autre", "jeton-autre")
	if rec := doAs(h, "GET", "/v1/info", "secret-token", "autre-machine-02", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("identifiant d'une autre machine : code %d, attendu 401", rec.Code)
	}
	if rec := doAs(h, "GET", "/v1/info", "secret-token", "client-machine-01", ""); rec.Code != http.StatusOK {
		t.Errorf("identifiant juste : code %d, attendu 200", rec.Code)
	}
	if rec := doAs(h, "GET", "/v1/info", "secret-token", "", ""); rec.Code != http.StatusOK {
		t.Errorf("sans en-tête X-Wsq-Machine : code %d, attendu 200", rec.Code)
	}
}

// TestStopGraceFitsStopWait : le délai de grâce de l'arrêt propre tient dans l'attente de
// « --agent stop ».
func TestStopGraceFitsStopWait(t *testing.T) {
	if defaultStopGrace >= agentStopWait {
		t.Fatalf("délai de grâce %s : doit tenir dans l'attente de --agent stop (%s)", defaultStopGrace, agentStopWait)
	}
}

// TestManagerUIURL : adresse de l'interface donnée par le manager à sa déclaration.
func TestManagerUIURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"": true, "http://10.0.0.5:8765": true, "https://manager.lan/wsq/": true,
		"ftp://x": false, "javascript:alert(1)": false, "http://": false, "http://u:p@h": false,
	} {
		if _, err := managerUIURL(raw); (err == nil) != ok {
			t.Errorf("managerUIURL(%q) : erreur %v, valide attendu %v", raw, err, ok)
		}
	}
}

// TestSyncMaintenanceAction : l'agent appelle --sync comme action de maintenance, jeu facultatif.
func TestSyncMaintenanceAction(t *testing.T) {
	if spec, ok := maintenanceActions["sync"]; !ok || spec.game != gameOptional || spec.args[0] != "--sync" {
		t.Errorf("« sync » de l'agent : %+v, attendu --sync avec jeu facultatif", spec)
	}
}

// TestPairCodeHostForms : l'hôte IPv6 est entre crochets dans le code (sinon le port se confond avec
// l'adresse) ; IPv4 et nom d'hôte restent tels quels. --sync wsq:// relit les trois.
func TestPairCodeHostForms(t *testing.T) {
	fp := strings.Repeat("AB", 32)
	for host, want := range map[string]string{
		"192.168.1.20":  "wsq://jeton_1@192.168.1.20:8790#" + fp,
		"poste-jeu":     "wsq://jeton_1@poste-jeu:8790#" + fp,
		"2001:db8::1":   "wsq://jeton_1@[2001:db8::1]:8790#" + fp,
		"fd00:1:2:3::9": "wsq://jeton_1@[fd00:1:2:3::9]:8790#" + fp,
	} {
		code := pairCode("jeton_1", host, 8790, fp)
		if code != want {
			t.Errorf("hôte %s : %q, attendu %q", host, code, want)
		}
		p, err := savesync.ParsePairCode(code)
		wantURL := "https://" + net.JoinHostPort(host, "8790")
		if err != nil || p.URL != wantURL {
			t.Errorf("hôte %s relu : %+v, %v (attendu %s)", host, p, err, wantURL)
		}
	}
}
