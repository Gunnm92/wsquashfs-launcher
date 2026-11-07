package main

// Téléchargements, archives, fichiers et commandes système de l'installeur
// (install.sh, section 3). Stdlib seulement. Les téléchargements passent par
// curl (repli : wget), comme dans le script bash : net/http ajoutait environ
// 2 Mo au binaire. zip par archive/zip ; tar, cp, sha512sum et les outils
// système sont appelés comme le faisait le script.

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// downloader : curl, sinon wget, sinon "" (aucun outil).
func downloader() string {
	if _, err := exec.LookPath("curl"); err == nil {
		return "curl"
	}
	if _, err := exec.LookPath("wget"); err == nil {
		return "wget"
	}
	return ""
}

// downloadTo télécharge url dans dest (affichage de progression sur le terminal).
func downloadTo(url, dest string) error {
	var cmd *exec.Cmd
	switch downloader() {
	case "curl":
		cmd = exec.Command("curl", "-fL", "--progress-bar", "-o", dest, url)
	case "wget":
		cmd = exec.Command("wget", "--show-progress", "-q", "-O", dest, url)
	default:
		return fmt.Errorf("curl ou wget requis")
	}
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// downloadBytes télécharge url en mémoire (silencieux, échec si HTTP >= 400).
func downloadBytes(url string) ([]byte, error) {
	var cmd *exec.Cmd
	switch downloader() {
	case "curl":
		cmd = exec.Command("curl", "-fsSL", "-A", "wsquashfs-launcher-installeur", url)
	case "wget":
		cmd = exec.Command("wget", "-qO-", "--user-agent=wsquashfs-launcher-installeur", url)
	default:
		return nil, fmt.Errorf("curl ou wget requis")
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// urlExists : vrai si le lien répond (simulation : rien n'est téléchargé).
func urlExists(url string) bool {
	switch downloader() {
	case "curl":
		return exec.Command("curl", "-fsIL", "-o", "/dev/null", url).Run() == nil
	case "wget":
		return exec.Command("wget", "-q", "--spider", url).Run() == nil
	}
	return false
}

// githubLatestTag : dernière version publiée d'un dépôt ; fallback si l'API
// ne répond pas.
func githubLatestTag(repo, fallback string) string {
	body, err := downloadBytes("https://api.github.com/repos/" + repo + "/releases/latest")
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

// fileSHA256 : empreinte SHA-256 d'un fichier, en hexadécimal minuscule.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// githubAssetSHA256 : empreinte SHA-256 qu'affiche GitHub pour un fichier
// d'une version publiée (champ digest de l'API), "" si inconnue (API
// injoignable, version publiée avant que GitHub ne les calcule).
func githubAssetSHA256(repo, tag, file string) string {
	body, err := downloadBytes("https://api.github.com/repos/" + repo + "/releases/tags/" + tag)
	if err != nil {
		return ""
	}
	var rel struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if json.Unmarshal(body, &rel) != nil {
		return ""
	}
	for _, a := range rel.Assets {
		if a.Name == file && strings.HasPrefix(a.Digest, "sha256:") {
			return strings.ToLower(strings.TrimPrefix(a.Digest, "sha256:"))
		}
	}
	return ""
}

// verifyGithubAsset : archive conforme à l'empreinte publiée par GitHub.
// Faux si elle diffère ; vrai avec un avertissement si GitHub n'en donne pas.
func verifyGithubAsset(archive, repo, tag, file string) bool {
	want := githubAssetSHA256(repo, tag, file)
	if want == "" {
		printInfo(file + " : empreinte non publiée par GitHub, archive non vérifiée")
		return true
	}
	got, err := fileSHA256(archive)
	if err != nil || got != want {
		printError(file + " : somme de contrôle SHA-256 incorrecte, archive ignorée")
		return false
	}
	return true
}

// fetch : téléchargement de label depuis url dans dest ; en simulation, seul
// le lien est vérifié.
func fetch(url, dest, label string) error {
	if dryRun {
		if urlExists(url) {
			fmt.Println("    [simulation] téléchargement de " + label + " : " + url)
			return nil
		}
		printError(label + " introuvable : " + url)
		return fmt.Errorf("lien introuvable : %s", url)
	}
	printInfo("Téléchargement de " + label + "...")
	if err := downloadTo(url, dest); err != nil {
		printError("Téléchargement échoué : " + url)
		return err
	}
	return nil
}

// runRoot : commande système (sudo hors root) ; en simulation, seulement
// affichée.
func runRoot(args ...string) error {
	full := append(append([]string{}, sudoCmd...), args...)
	if dryRun {
		fmt.Println("    [simulation] " + strings.Join(full, " "))
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
		return err
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
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
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
		return err
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

// copyFile copie src dans dst avec le mode donné.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
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

// findIn : premier chemin (entrées de dossiers et fichiers) accepté par match,
// parcouru dans chaque racine jusqu'à maxDepth niveaux (find -maxdepth).
// Équivalent du find | grep du script ; ordre lexical.
func findIn(roots []string, maxDepth int, match func(path string) bool) string {
	for _, root := range roots {
		found := ""
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if rel == "." {
				return nil
			}
			if strings.Count(rel, string(os.PathSeparator))+1 > maxDepth {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if match(p) {
				found = p
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found
		}
	}
	return ""
}

// mkTempDir : dossier temporaire neuf, vide.
func mkTempDir() (string, error) {
	return os.MkdirTemp("", "wsq-setup.")
}
