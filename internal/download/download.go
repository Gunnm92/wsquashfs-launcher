// Package download : téléchargements par curl (repli : wget) et empreintes
// SHA-256 de fichiers. net/http ajoutait environ 2 Mo au binaire : les outils
// système font le travail, comme le faisait install.sh.
package download

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// File télécharge url dans dest (affichage de progression sur le terminal).
func File(url, dest string) error {
	var cmd *exec.Cmd
	switch downloader() {
	case "curl":
		cmd = exec.Command("curl", "-fL", "--progress-bar", "-o", dest, url)
	case "wget":
		cmd = exec.Command("wget", "--show-progress", "-q", "-O", dest, url)
	default:
		return errors.New("curl ou wget requis")
	}
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Bytes télécharge url en mémoire (silencieux, échec si HTTP >= 400).
func Bytes(url string) ([]byte, error) {
	var cmd *exec.Cmd
	switch downloader() {
	case "curl":
		cmd = exec.Command("curl", "-fsSL", "-A", "wsquashfs-launcher-installeur", url)
	case "wget":
		cmd = exec.Command("wget", "-qO-", "--user-agent=wsquashfs-launcher-installeur", url)
	default:
		return nil, errors.New("curl ou wget requis")
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("téléchargement de %s : %w", url, err)
	}
	return out.Bytes(), nil
}

// Exists : vrai si le lien répond (rien n'est téléchargé).
func Exists(url string) bool {
	switch downloader() {
	case "curl":
		return exec.Command("curl", "-fsIL", "-o", "/dev/null", url).Run() == nil
	case "wget":
		return exec.Command("wget", "-q", "--spider", url).Run() == nil
	}
	return false
}

// SHA256 : empreinte SHA-256 d'un fichier, en hexadécimal minuscule.
func SHA256(path string) (string, error) {
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
