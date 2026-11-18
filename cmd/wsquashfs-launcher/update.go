package main

// Commande --update : mise à jour du lanceur depuis GitHub (empreinte SHA-256 et en-tête ELF).

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"

	"wsquashfs-launcher/internal/ui"
)

// ── --update ───────────────────────────────────────────────────────────

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

// launcherBuild : version du lanceur en cours, c'est-à-dire le début de l'empreinte SHA-256 du
// binaire (la même que celle que publie wsquashfs-launcher.sha256). « inconnue » si illisible.
func launcherBuild() string {
	self, err := os.Executable()
	if err != nil {
		return "inconnue"
	}
	cur, err := fileSHA256(self)
	if err != nil {
		return "inconnue"
	}
	return shortBuild(cur)
}

// shortBuild : 12 premiers caractères d'une empreinte, de quoi la comparer à l'œil.
func shortBuild(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// cmdUpdate : met à jour le lanceur depuis GitHub. Le binaire publié
// s'accompagne de wsquashfs-launcher.sha256 (make build) : déjà identique,
// rien n'est téléchargé ; sinon le fichier téléchargé doit avoir cette
// empreinte et un en-tête ELF avant de remplacer le lanceur.
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
	cur, curErr := fileSHA256(self)
	if curErr != nil {
		cur = ""
	}
	ui.KV(0, ui.Pair{"Version installée", shortBuild(cur)}, ui.Pair{"Version distribuée", shortBuild(want)})
	if cur == want {
		ui.StatusLine(ui.OK, "Lanceur déjà à jour : "+self)
		return 0
	}
	tmp, err := os.MkdirTemp("", "wsq-update.")
	if err != nil {
		die("dossier temporaire impossible")
	}
	defer os.RemoveAll(tmp)
	ui.StatusLine(ui.Info, "Téléchargement depuis "+base+" ...")
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
	ui.StatusLine(ui.OK, "Lanceur mis à jour : "+self+" (version "+shortBuild(want)+")")
	removeAgentUnits() // démarrage automatique par systemd des anciennes versions
	ui.KV(0, ui.Pair{"Vérification", os.Args[0] + " --help"})
	return 0
}
