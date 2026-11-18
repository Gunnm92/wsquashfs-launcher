package setup

// Téléchargements, archives, fichiers et commandes système de l'installeur
// (install.sh, section 3). Stdlib seulement. Les téléchargements passent par
// curl (repli : wget), comme dans le script bash : net/http ajoutait environ
// 2 Mo au binaire. zip par archive/zip ; tar, cp, sha512sum et les outils
// système sont appelés comme le faisait le script.

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"wsquashfs-launcher/internal/download"
	"wsquashfs-launcher/internal/ui"
)

// githubLatestTag : dernière version publiée d'un dépôt ; fallback si l'API
// ne répond pas.
func githubLatestTag(repo, fallback string) string {
	body, err := download.Bytes("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return fallback
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if json.Unmarshal(body, &rel) != nil || rel.TagName == "" {
		return fallback
	}
	return rel.TagName
}

// githubAssetSHA256 : empreinte SHA-256 qu'affiche GitHub pour un fichier
// d'une version publiée (champ digest de l'API). "" si GitHub n'en publie pas
// pour ce fichier (version antérieure à leur calcul) ; reachable faux si
// l'API n'a pas répondu (panne, limite de débit) : l'empreinte est alors
// inconnue, pas absente.
func githubAssetSHA256(repo, tag, file string) (digest string, reachable bool) {
	body, err := download.Bytes("https://api.github.com/repos/" + repo + "/releases/tags/" + tag)
	if err != nil {
		return "", false
	}
	var rel struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if json.Unmarshal(body, &rel) != nil {
		return "", false
	}
	for _, a := range rel.Assets {
		if a.Name == file && strings.HasPrefix(a.Digest, "sha256:") {
			return strings.ToLower(strings.TrimPrefix(a.Digest, "sha256:")), true
		}
	}
	return "", true
}

// verifyGithubAsset : archive conforme à l'empreinte publiée par GitHub.
// Faux si elle diffère. GitHub n'en publie pas pour ce fichier : acceptée
// avec un avertissement. API injoignable : refusée, sauf accord donné au
// terminal (jamais avec yes ni sans terminal).
func verifyGithubAsset(archive, repo, tag, file string) bool {
	want, reachable := githubAssetSHA256(repo, tag, file)
	if !reachable {
		setupErr(file + " : empreinte introuvable (API GitHub injoignable ou limite de débit)")
		if assumeYes || !askYesNo("Installer "+file+" sans vérification ?", false) {
			setupSkip(file + " ignoré : relancer l'installation plus tard")
			return false
		}
		return true
	}
	if want == "" {
		ui.Item(ui.Skip, file+" : empreinte non publiée par GitHub, archive non vérifiée")
		return true
	}
	got, err := download.SHA256(archive)
	if err != nil || got != want {
		setupErr(file + " : somme de contrôle SHA-256 incorrecte, archive ignorée")
		return false
	}
	return true
}

// fetch : téléchargement de label depuis url dans dest ; en simulation, seul
// le lien est vérifié.
func fetch(url, dest, label string) error {
	if dryRun {
		if download.Exists(url) {
			simLine("téléchargement de " + label + " : " + url)
			return nil
		}
		setupErr(label + " introuvable : " + url)
		return fmt.Errorf("lien introuvable : %s", url)
	}
	ui.Item(ui.Info, "Téléchargement de "+label+"...")
	if err := download.File(url, dest); err != nil {
		setupErr("Téléchargement échoué : " + url)
		return err
	}
	return nil
}

// runRoot : commande système (sudo hors root) ; en simulation, seulement
// affichée.
func runRoot(args ...string) error {
	full := append(append([]string{}, sudoCmd...), args...)
	if dryRun {
		simLine(strings.Join(full, " "))
		return nil
	}
	return runCmd(full[0], full[1:]...)
}

// runCmd exécute une commande avec le terminal pour sortie et entrée.
func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// cmdOK : vrai si la commande réussit (sortie ignorée).
func cmdOK(name string, args ...string) bool {
	return execQuiet(name, args...) == nil
}

// extract : archive (zip, tar, tar.zst, tar.xz...) dans dest.
func extract(archive, dest string) error {
	switch {
	case strings.HasSuffix(archive, ".zip"):
		return extractZip(archive, dest)
	case strings.HasSuffix(archive, ".tar.zst"):
		return runCmd("tar", "--zstd", "-xf", archive, "-C", dest)
	default:
		return runCmd("tar", "-xf", archive, "-C", dest)
	}
}

func extractZip(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("lecture de l'archive zip : %w", err)
	}
	defer zr.Close()
	clean := filepath.Clean(dest) + string(os.PathSeparator)
	for _, f := range zr.File {
		target := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(target, clean) {
			return fmt.Errorf("chemin hors du dossier : %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeZipEntry(f, target); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("entrée %s : %w", f.Name, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return fmt.Errorf("extraction de %s : %w", target, err)
	}
	return out.Close()
}

// topDirOf : premier dossier de dir (racine d'une archive extraite), "" sinon.
func topDirOf(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// installTree remplace dst par src. Copie locale, sans liens durs : les
// systèmes de fichiers réseau (shfs Unraid, CIFS…) ne les prennent pas. La
// copie se fait dans dst.new : l'ancienne version n'est retirée qu'une fois
// la nouvelle complète (une copie ratée laisse dst intact).
func installTree(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	fresh, old := dst+".new", dst+".old"
	_ = os.RemoveAll(fresh)
	if err := runCmd("cp", "-rp", src, fresh); err != nil {
		_ = os.RemoveAll(fresh)
		return fmt.Errorf("copie de %s : %w", src, err)
	}
	_ = os.RemoveAll(old)
	if pathExists(dst) {
		if err := os.Rename(dst, old); err != nil {
			_ = os.RemoveAll(fresh)
			return err
		}
	}
	if err := os.Rename(fresh, dst); err != nil {
		_ = os.Rename(old, dst)
		_ = os.RemoveAll(fresh)
		return err
	}
	return os.RemoveAll(old)
}

// isFile : fichier ordinaire (test -f).
func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// isExecutable : fichier ordinaire exécutable (test -x).
func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// mkTempDir : dossier temporaire neuf, vide.
func mkTempDir() (string, error) {
	return os.MkdirTemp("", "wsq-setup.")
}

// execQuiet : exécute la commande, sans afficher sa sortie.
func execQuiet(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}
