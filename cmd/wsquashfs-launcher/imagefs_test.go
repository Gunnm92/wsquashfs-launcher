package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	squashfslow "github.com/CalebQ42/squashfs/low"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// sqTestImage : image construite par mksquashfs (ignoré sans lui).
func sqTestImage(t *testing.T, comp string, fill func(dir string)) *squashImage {
	t.Helper()
	if _, err := exec.LookPath("mksquashfs"); err != nil {
		t.Skip("mksquashfs absent")
	}
	src := filepath.Join(t.TempDir(), "src")
	_ = os.MkdirAll(src, 0o755)
	fill(src)
	img := filepath.Join(t.TempDir(), "t.sqfs")
	if out, err := exec.Command("mksquashfs", src, img, "-comp", comp, "-noappend", "-no-xattrs", "-quiet", "-no-progress").CombinedOutput(); err != nil {
		t.Fatalf("mksquashfs : %v %s", err, out)
	}
	sq, err := openSquashImage(img, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sq.file.Close() })
	return sq
}

// sqTestNode : entrée de la racine, sans passer par le noyau.
func sqTestNode(t *testing.T, sq *squashImage, name string) *sqNode {
	t.Helper()
	for _, e := range sq.rdr.Root.Entries {
		if e.Name == name {
			in, err := sq.inodeAt(e.BlockStart, e.Offset)
			if err != nil {
				t.Fatal(err)
			}
			return &sqNode{img: sq, base: squashfslow.FileBase{Inode: in, Name: name}}
		}
	}
	t.Fatalf("%s absent de l'image", name)
	return nil
}

func readNodeAll(t *testing.T, n *sqNode, chunk int) []byte {
	t.Helper()
	var out []byte
	for off := int64(0); ; {
		res, errno := n.Read(context.Background(), nil, make([]byte, chunk), off)
		if errno != 0 {
			t.Fatalf("lecture à %d : %v", off, errno)
		}
		b, _ := res.Bytes(nil)
		if len(b) == 0 {
			return out
		}
		out = append(out, b...)
		off += int64(len(b))
	}
}

// Bloc creux en fin de fichier dont la taille est un multiple de celle des
// blocs : la bibliothèque rendait 0 octet (EIO sur un fichier de Nosferatu) ; fragments
// partagés entre fichiers (cache par position).
func TestImageSparseAndFragments(t *testing.T) {
	big := make([]byte, 4*131072)
	big[200000] = 7 // 2ᵉ bloc non nul, les autres creux
	files := map[string][]byte{
		"sparse": big,
		"small":  []byte("fragment de fin de fichier"),
		"small2": []byte("autre fichier, même bloc de fragments"),
		"odd":    bytes.Repeat([]byte("ab"), 70000), // un bloc complet + fragment
	}
	for _, comp := range []string{"zstd", "gzip"} {
		sq := sqTestImage(t, comp, func(d string) {
			for name, data := range files {
				_ = os.WriteFile(filepath.Join(d, name), data, 0o644)
			}
		})
		for name, want := range files {
			for _, chunk := range []int{4096, 131072, 200000} {
				if got := readNodeAll(t, sqTestNode(t, sq, name), chunk); !bytes.Equal(got, want) {
					t.Errorf("%s %s (lectures de %d) : %d octets lus, %d attendus ou contenu différent", comp, name, chunk, len(got), len(want))
				}
			}
		}
	}
}

// lstat d'un lien : taille = longueur de la cible (comme squashfuse).
func TestImageSymlinkAttr(t *testing.T) {
	sq := sqTestImage(t, "zstd", func(d string) {
		_ = os.Symlink("../drive_c", filepath.Join(d, "c:"))
	})
	n := sqTestNode(t, sq, "c:")
	var a fuse.Attr
	n.fillAttr(&a)
	if a.Mode&syscall.S_IFMT != syscall.S_IFLNK || a.Size != uint64(len("../drive_c")) {
		t.Errorf("mode %o taille %d", a.Mode, a.Size)
	}
	if target, errno := n.Readlink(context.Background()); errno != 0 || string(target) != "../drive_c" {
		t.Errorf("cible %q (%v)", target, errno)
	}
}

func TestParseServeImage(t *testing.T) {
	o, err := parseServeImage([]string{"img", "pt", "uid", "1000", "gid", "100", "allow_other"})
	if err != nil || o.image != "img" || o.point != "pt" || o.uid != 1000 || o.gid != 100 || !o.allowOther {
		t.Errorf("%+v %v", o, err)
	}
	if o, _ := parseServeImage([]string{"img", "pt"}); o.uid != -1 || o.gid != -1 || o.allowOther {
		t.Errorf("par défaut : %+v", o)
	}
	for _, bad := range [][]string{{"img"}, {"img", "pt", "uid"}, {"img", "pt", "uid", "x"}, {"a", "b", "c"}} {
		if _, err := parseServeImage(bad); err == nil {
			t.Errorf("%v accepté", bad)
		}
	}
}

// Ligne Exec du .thumbnailer, exécutée comme le ferait tumbler (%i, %o
// remplacés par des chemins quotés) : l'illustration sort telle quelle ; une
// image sans illustration échoue (miniature par défaut).
func TestThumbnailerExtractsCover(t *testing.T) {
	cover := []byte("\x89PNG\r\n\x1a\njaquette")
	img := filepath.Join(t.TempDir(), "Jeu d'essai.wsquashfs")
	src := filepath.Join(t.TempDir(), "src")
	_ = os.MkdirAll(filepath.Join(src, ".wsquashfs"), 0o755)
	_ = os.WriteFile(filepath.Join(src, imageCoverPath), cover, 0o644)
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
		cmd := strings.NewReplacer("%i", shQuote(in), "%o", shQuote(out)).Replace(execLine)
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
