package imagefs

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	squashfslow "github.com/CalebQ42/squashfs/low"
)

// benchImage : image de mesure (ignorée sans mksquashfs) : un gros fichier
// compressible (texte pseudo-aléatoire, 64 Mio) et 2000 petits fichiers
// (fragments), comme un jeu (ressources + scripts).
func benchImage(b *testing.B, comp string) string {
	b.Helper()
	if _, err := exec.LookPath("mksquashfs"); err != nil {
		b.Skip("mksquashfs absent")
	}
	src := filepath.Join(b.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "petits"), 0o755); err != nil {
		b.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 2))
	words := strings.Fields("texture modèle son niveau carte joueur ennemi arme décor lumière ombre script données")
	var sb strings.Builder
	for sb.Len() < 64<<20 {
		sb.WriteString(words[r.IntN(len(words))])
		sb.WriteByte(byte(' ' + r.IntN(3)))
		fmt.Fprintf(&sb, "%d\n", r.IntN(1<<20))
	}
	if err := os.WriteFile(filepath.Join(src, "gros.bin"), []byte(sb.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	for i := range 2000 {
		n := 512 + r.IntN(6000)
		if err := os.WriteFile(filepath.Join(src, "petits", fmt.Sprintf("f%04d.txt", i)), []byte(sb.String()[i*997:i*997+n]), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	img := filepath.Join(b.TempDir(), "b.sqfs")
	out, err := exec.Command("mksquashfs", src, img, "-comp", comp, "-noappend", "-quiet", "-no-progress", "-no-xattrs").CombinedOutput()
	if err != nil {
		b.Fatalf("mksquashfs : %v %s", err, out)
	}
	return img
}

func benchNode(b *testing.B, sq *squashImage, dir squashfslow.FileBase, name string) *sqNode {
	b.Helper()
	d, err := sq.readDir(dir.Inode)
	if err != nil {
		b.Fatal(err)
	}
	for _, e := range d {
		if e.Name == name {
			in, err := sq.inodeAt(e.BlockStart, e.Offset)
			if err != nil {
				b.Fatal(err)
			}
			return &sqNode{img: sq, base: squashfslow.FileBase{Inode: in, Name: name}}
		}
	}
	b.Fatalf("%s absent", name)
	return nil
}

func readAll(b *testing.B, n *sqNode, buf []byte) int64 {
	var total int64
	for off := int64(0); ; {
		res, errno := n.Read(context.Background(), nil, buf, off)
		if errno != 0 {
			b.Fatalf("lecture à %d : %v", off, errno)
		}
		got := res.Size()
		if got == 0 {
			return total
		}
		total += int64(got)
		off += int64(got)
	}
}

// BenchmarkReadCold : lecture séquentielle (requêtes de 1 Mio, comme FUSE)
// du gros fichier puis de tous les petits, image rouverte à chaque tour
// (caches vides : décompression comprise).
func BenchmarkReadCold(b *testing.B) {
	for _, comp := range []string{"zstd", "gzip", "xz", "lz4"} {
		b.Run(comp, func(b *testing.B) {
			img := benchImage(b, comp)
			buf := make([]byte, 1<<20)
			b.ReportAllocs()
			var bytes int64
			for b.Loop() {
				sq, err := openSquashImage(img, -1, -1)
				if err != nil {
					b.Fatal(err)
				}
				root := sq.rdr.Root.FileBase
				bytes = readAll(b, benchNode(b, sq, root, "gros.bin"), buf)
				petits := benchNode(b, sq, root, "petits")
				d, err := sq.readDir(petits.base.Inode)
				if err != nil {
					b.Fatal(err)
				}
				for _, e := range d {
					in, err := sq.inodeAt(e.BlockStart, e.Offset)
					if err != nil {
						b.Fatal(err)
					}
					bytes += readAll(b, &sqNode{img: sq, base: squashfslow.FileBase{Inode: in, Name: e.Name}}, buf)
				}
				sq.file.Close()
			}
			b.SetBytes(bytes)
		})
	}
}
