package setup

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Ligne Exec du .thumbnailer, exécutée comme le ferait tumbler (%i, %o
// remplacés par des chemins quotés) : l'illustration sort telle quelle ; une
// image sans illustration échoue (miniature par défaut).
func TestThumbnailerExtractsCover(t *testing.T) {
	old := env
	t.Cleanup(func() { env = old })
	env = Env{CoverPath: ".wsquashfs/cover"}
	cover := []byte("\x89PNG\r\n\x1a\njaquette")
	img := filepath.Join(t.TempDir(), "Jeu d'essai.wsquashfs")
	src := filepath.Join(t.TempDir(), "src")
	_ = os.MkdirAll(filepath.Join(src, ".wsquashfs"), 0o755)
	_ = os.WriteFile(filepath.Join(src, ".wsquashfs", "cover"), cover, 0o644)
	bare := filepath.Join(t.TempDir(), "nu.wsquashfs")
	for out, dir := range map[string]string{img: src, bare: t.TempDir()} {
		if _, err := exec.LookPath("mksquashfs"); err != nil {
			t.Skip("mksquashfs absent")
		}
		if b, err := exec.Command("mksquashfs", dir, out, "-noappend", "-quiet", "-no-progress").CombinedOutput(); err != nil {
			t.Fatalf("mksquashfs : %v %s", err, b)
		}
	}
	var execLine string
	for _, l := range strings.Split(thumbnailerContent(), "\n") {
		if strings.HasPrefix(l, "Exec=") {
			execLine = strings.TrimPrefix(l, "Exec=")
		}
	}
	if !strings.Contains(thumbnailerContent(), "MimeType=application/x-wsquashfs;") || execLine == "" {
		t.Fatalf("fichier .thumbnailer incomplet :\n%s", thumbnailerContent())
	}
	run := func(in string) ([]byte, error) {
		out := filepath.Join(t.TempDir(), "thumb.png")
		cmd := strings.NewReplacer("%i", shellQuote(in), "%o", shellQuote(out)).Replace(execLine)
		if err := exec.Command("sh", "-c", cmd).Run(); err != nil {
			return nil, err
		}
		return os.ReadFile(out)
	}
	if got, err := run(img); err != nil || !bytes.Equal(got, cover) {
		t.Errorf("illustration : %q (%v)", got, err)
	}
	if _, err := run(bare); err == nil {
		t.Error("image sans illustration : succès inattendu")
	}
}

// shellQuote : argument quoté pour /bin/sh (apostrophes internes échappées).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
