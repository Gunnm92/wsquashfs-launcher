package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestShQuoteRoundTrip : shQuote reste un seul argument pour /bin/sh, même avec
// apostrophes et espaces.
func TestShQuoteRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh absent")
	}
	for _, in := range []string{"/tmp/simple.png", "/tmp/d'ap pli.png", "/tmp/''x  y'"} {
		out, err := exec.Command("sh", "-c", "printf %s "+shQuote(in)).Output()
		if err != nil || string(out) != in {
			t.Errorf("shQuote(%q) : sh rend %q (err %v)", in, out, err)
		}
	}
}

// TestPackCoverAddsIllustration : --pack … cover FICHIER range l'illustration à
// .wsquashfs/cover de l'image, sans toucher au dossier source.
func TestPackCoverAddsIllustration(t *testing.T) {
	for _, bin := range []string{"mksquashfs", "unsquashfs"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " absent")
		}
	}
	dir := t.TempDir()
	conf.savesDir = filepath.Join(dir, "saves")

	src := filepath.Join(dir, "Mon jeu d'essai.wine")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "autorun.cmd"), []byte("echo ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(dir, "jaq uette d'art.png")
	coverData := []byte("\x89PNG\r\n\x1a\nxyz\x00\x01")
	if err := os.WriteFile(cover, coverData, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "sortie.wsquashfs")

	if rc := packPrefix([]string{src, out, "cover", cover}); rc != 0 {
		t.Fatalf("packPrefix rc = %d", rc)
	}
	got, err := exec.Command("unsquashfs", "-cat", out, imageCoverPath).Output()
	if err != nil {
		t.Fatalf("unsquashfs -cat : %v", err)
	}
	if !bytes.Equal(got, coverData) {
		t.Errorf("illustration différente : %q, attendu %q", got, coverData)
	}
	if _, err := os.Stat(filepath.Join(src, ".wsquashfs")); !os.IsNotExist(err) {
		t.Errorf("le dossier source a été modifié (.wsquashfs présent, err %v)", err)
	}
	if !isFile(filepath.Join(src, "autorun.cmd")) {
		t.Errorf("autorun.cmd du dossier source disparu")
	}
}
