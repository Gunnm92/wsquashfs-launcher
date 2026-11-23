package agent

// Réglages lus et écrits par le manager (GET /v1/config, PUT /v1/config/defaults,
// PUT /v1/config/optiscaler) : le modèle des défauts (defaults.autorun, machine)
// et le modèle partiel d'OptiScaler.ini, plus l'état des cartes et de
// l'environnement WSQUASHFS_* de l'agent. Le lanceur lit les mêmes fichiers
// (layers.go, optiscaler.go) : leur forme est codée des deux côtés.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	configFileMax = 64 << 10  // taille maximale d'un fichier de réglages (64 Kio)
	configBodyMax = 512 << 10 // corps d'une écriture : le JSON échappe le texte
)

var (
	defaultsLigne    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=.*$`)
	optiscalerLigne  = regexp.MustCompile(`^[A-Za-z0-9_.]+\s*=.*$`)
	optiscalerSecion = regexp.MustCompile(`^\[[^\[\]]+\]$`)
)

// configFichier : un fichier de réglages : son chemin et la forme de ses lignes utiles.
type configFichier struct {
	path  string
	valid func(ligne string) error // ligne déjà sans espaces de bord, non vide, non commentaire
}

// optiscalerBaseDir : OptiScaler installé par --setup (même chemin que le lanceur).
func optiscalerBaseDir() string {
	return filepath.Join(os.Getenv("HOME"), ".local/share/wsquashfs/optiscaler")
}

// defaultsFichier : modèle machine des défauts (couche « machine » du lanceur).
func defaultsFichier() configFichier {
	return configFichier{
		path: filepath.Join(configHome(), "wsquashfs", "defaults.autorun"),
		valid: func(l string) error {
			if !defaultsLigne.MatchString(l) {
				return errors.New("forme attendue : CLÉ=valeur")
			}
			return nil
		},
	}
}

// optiscalerIniFichier : modèle partiel machine d'OptiScaler.ini (optiscaler.go).
func optiscalerIniFichier() configFichier {
	return configFichier{
		path: filepath.Join(configHome(), "wsquashfs", "optiscaler.ini"),
		valid: func(l string) error {
			if !optiscalerSecion.MatchString(l) && !optiscalerLigne.MatchString(l) {
				return errors.New("forme attendue : [Section] ou Clé=valeur")
			}
			return nil
		},
	}
}

// configLire : contenu d'un fichier de réglages et sa sha256 (nil si absent).
func configLire(path string) (string, *string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	return string(data), &sha, nil
}

// configValider : taille, octet NUL, puis chaque ligne utile ; le message cite le numéro de ligne.
func configValider(texte string, f configFichier) error {
	if len(texte) > configFileMax {
		return errors.New("fichier trop volumineux (64 Kio au plus)")
	}
	if strings.ContainsRune(texte, 0) {
		return errors.New("octet NUL interdit")
	}
	for i, ligne := range strings.Split(texte, "\n") {
		t := strings.TrimSpace(ligne)
		if t == "" || t[0] == ';' || t[0] == '#' {
			continue
		}
		if err := f.valid(t); err != nil {
			return fmt.Errorf("ligne %d : %v", i+1, err)
		}
	}
	return nil
}

// ecrireAtomique : fichier temporaire du même dossier, puis rename ; dossier créé en 0755,
// fichier en 0644.
func ecrireAtomique(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // sans effet une fois renommé
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// configGet : GET /v1/config.
func (s *service) configGet(w http.ResponseWriter, r *http.Request) {
	defTexte, defSha, err := configLire(defaultsFichier().path)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "lecture des défauts impossible")
		return
	}
	ovTexte, ovSha, err := configLire(optiscalerIniFichier().path)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "lecture de OptiScaler.ini impossible")
		return
	}
	installe := isFile(filepath.Join(optiscalerBaseDir(), "OptiScaler.dll"))
	version, template := "", ""
	if installe {
		if v, err := os.ReadFile(filepath.Join(optiscalerBaseDir(), "VERSION")); err == nil {
			version = strings.TrimSpace(string(v))
		}
		if t, err := os.ReadFile(filepath.Join(optiscalerBaseDir(), "OptiScaler.ini")); err == nil {
			template = string(t)
		}
	}
	nvidia, amd := gpusPresents()
	writeJSON(w, http.StatusOK, map[string]any{
		"gpu":      map[string]bool{"nvidia": nvidia, "amd": amd},
		"defaults": map[string]any{"path": defaultsFichier().path, "text": defTexte, "sha": nullable(defSha)},
		"optiscaler": map[string]any{
			"installed": installe,
			"version":   version,
			"template":  template,
			"overrides": map[string]any{"path": optiscalerIniFichier().path, "text": ovTexte, "sha": nullable(ovSha)},
		},
		"env": environnementWsquashfs(),
	})
}

// nullable : la valeur pointée, ou nil (JSON null).
func nullable(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// gpusPresents : carte NVIDIA (0x10de) et AMD (0x1002) d'après les fournisseurs DRM.
// WSQUASHFS_DRM_SYSFS remplace /sys/class/drm (tests), comme pour le lanceur.
func gpusPresents() (nvidia, amd bool) {
	racine := os.Getenv("WSQUASHFS_DRM_SYSFS")
	if racine == "" {
		racine = "/sys/class/drm"
	}
	vendeurs, _ := filepath.Glob(racine + "/card*/device/vendor")
	for _, v := range vendeurs {
		data, err := os.ReadFile(v)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(data)) {
		case "0x10de":
			nvidia = true
		case "0x1002":
			amd = true
		}
	}
	return nvidia, amd
}

// environnementWsquashfs : variables WSQUASHFS_* de l'agent, sans celles dont le nom contient
// TOKEN, SECRET, KEY ou PASS.
func environnementWsquashfs() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		nom, valeur, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(nom, "WSQUASHFS_") {
			continue
		}
		if secretNom(nom) {
			continue
		}
		env[nom] = valeur
	}
	return env
}

// secretNom : le nom de variable évoque un secret.
func secretNom(nom string) bool {
	u := strings.ToUpper(nom)
	for _, m := range []string{"TOKEN", "SECRET", "KEY", "PASS"} {
		if strings.Contains(u, m) {
			return true
		}
	}
	return false
}

// configDefaultsPut : PUT /v1/config/defaults {text, base_sha}.
func (s *service) configDefaultsPut(w http.ResponseWriter, r *http.Request) {
	s.configPut(w, r, defaultsFichier())
}

// configOptiscalerPut : PUT /v1/config/optiscaler {text, base_sha}.
func (s *service) configOptiscalerPut(w http.ResponseWriter, r *http.Request) {
	s.configPut(w, r, optiscalerIniFichier())
}

// configPut : écriture d'un fichier de réglages, si son contenu est celui que le client a lu
// (base_sha, 409 sinon). Un texte vide supprime le fichier.
func (s *service) configPut(w http.ResponseWriter, r *http.Request, f configFichier) {
	r.Body = http.MaxBytesReader(w, r.Body, configBodyMax)
	var req struct {
		Text    *string `json:"text"`
		BaseSHA *string `json:"base_sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var trop *http.MaxBytesError
		if errors.As(err, &trop) {
			writeDetail(w, http.StatusRequestEntityTooLarge, "corps trop volumineux")
			return
		}
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	if req.Text == nil {
		writeDetail(w, http.StatusBadRequest, "requête illisible")
		return
	}
	texte := *req.Text
	vide := strings.TrimSpace(texte) == ""
	if !vide {
		if err := configValider(texte, f); err != nil {
			writeDetail(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	_, actuelle, err := configLire(f.path)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "lecture impossible")
		return
	}
	if !memeSha(actuelle, req.BaseSHA) {
		writeDetail(w, http.StatusConflict, "fichier modifié depuis la lecture")
		return
	}
	if vide {
		if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			writeDetail(w, http.StatusInternalServerError, "suppression impossible")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sha": nil})
		return
	}
	if err := ecrireAtomique(f.path, []byte(texte)); err != nil {
		writeDetail(w, http.StatusInternalServerError, "écriture impossible")
		return
	}
	_, sha, _ := configLire(f.path)
	writeJSON(w, http.StatusOK, map[string]any{"sha": nullable(sha)})
}

// memeSha : même empreinte (nil égale nil : fichier absent des deux côtés).
func memeSha(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
