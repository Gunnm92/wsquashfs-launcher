package imagefs

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
	"golang.org/x/sys/unix"
)

// sqTestImage : image construite par mksquashfs (ignoré sans lui).
func sqTestImage(t *testing.T, comp string, fill func(dir string), keepXattrs ...bool) *squashImage {
	t.Helper()
	if _, err := exec.LookPath("mksquashfs"); err != nil {
		t.Skip("mksquashfs absent")
	}
	src := filepath.Join(t.TempDir(), "src")
	_ = os.MkdirAll(src, 0o755)
	fill(src)
	img := filepath.Join(t.TempDir(), "t.sqfs")
	args := []string{src, img, "-comp", comp, "-noappend", "-quiet", "-no-progress"}
	if len(keepXattrs) == 0 || !keepXattrs[0] {
		args = append(args, "-no-xattrs")
	}
	if out, err := exec.Command("mksquashfs", args...).CombinedOutput(); err != nil {
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
	o, err := ParseArgs([]string{"img", "pt", "uid", "1000", "gid", "100", "allow_other"})
	if err != nil || o.Image != "img" || o.Point != "pt" || o.UID != 1000 || o.GID != 100 || !o.AllowOther {
		t.Errorf("%+v %v", o, err)
	}
	if o, _ := ParseArgs([]string{"img", "pt"}); o.UID != -1 || o.GID != -1 || o.AllowOther {
		t.Errorf("par défaut : %+v", o)
	}
	for _, bad := range [][]string{{"img"}, {"img", "pt", "uid"}, {"img", "pt", "uid", "x"}, {"a", "b", "c"}} {
		if _, err := ParseArgs(bad); err == nil {
			t.Errorf("%v accepté", bad)
		}
	}
}

// Attributs étendus de l'image (user.DOSATTRIB de Wine, valeur binaire,
// grande valeur partagée stockée à part) : rendus comme squashfuse ; un
// fichier sans attribut répond ENODATA.
func TestImageXattrs(t *testing.T) {
	big := bytes.Repeat([]byte("partagé "), 40)
	sq := sqTestImageXattr(t, map[string]map[string][]byte{
		"a.exe":    {"user.DOSATTRIB": []byte("0x20")},
		"b.bin":    {"user.bin": {0, 0xff, 0x10}, "user.DOSATTRIB": []byte("0x22")},
		"c1":       {"user.grand": big},
		"c2":       {"user.grand": big},
		"c3":       {"user.grand": big},
		"rien.txt": nil,
	})
	for name, want := range map[string]map[string][]byte{
		"a.exe": {"user.DOSATTRIB": []byte("0x20")},
		"b.bin": {"user.bin": {0, 0xff, 0x10}, "user.DOSATTRIB": []byte("0x22")},
		"c2":    {"user.grand": big},
	} {
		n := sqTestNode(t, sq, name)
		for attr, v := range want {
			buf := make([]byte, 1024)
			sz, errno := n.Getxattr(context.Background(), attr, buf)
			if errno != 0 || !bytes.Equal(buf[:sz], v) {
				t.Errorf("%s %s : %q (%v)", name, attr, buf[:sz], errno)
			}
			if sz, errno := n.Getxattr(context.Background(), attr, nil); errno != syscall.ERANGE || int(sz) != len(v) {
				t.Errorf("%s %s : taille %d (%v)", name, attr, sz, errno)
			}
		}
		buf := make([]byte, 1024)
		sz, _ := n.Listxattr(context.Background(), buf)
		if got := strings.Count(string(buf[:sz]), "\x00"); got != len(want) {
			t.Errorf("%s : %d noms listés, %d attendus (%q)", name, got, len(want), buf[:sz])
		}
	}
	if _, errno := sqTestNode(t, sq, "rien.txt").Getxattr(context.Background(), "user.DOSATTRIB", make([]byte, 64)); errno != syscall.ENODATA {
		t.Errorf("sans attribut : %v", errno)
	}
}

func sqTestImageXattr(t *testing.T, files map[string]map[string][]byte) *squashImage {
	t.Helper()
	return sqTestImage(t, "zstd", func(d string) {
		for name, attrs := range files {
			p := filepath.Join(d, name)
			_ = os.WriteFile(p, []byte(name), 0o644)
			for k, v := range attrs {
				if err := unix.Setxattr(p, k, v, 0); err != nil {
					t.Skip("attributs user.* impossibles ici : " + err.Error())
				}
			}
		}
	}, true)
}

// sqReadAll : flux plus court que le bloc rendu tel quel, flux trop long refusé.
func TestSqReadAll(t *testing.T) {
	if got, err := sqReadAll(bytes.NewReader([]byte("abc")), 8); err != nil || string(got) != "abc" {
		t.Errorf("flux court : %q, %v", got, err)
	}
	if got, err := sqReadAll(bytes.NewReader([]byte("12345678")), 8); err != nil || string(got) != "12345678" {
		t.Errorf("flux exact : %q, %v", got, err)
	}
	if _, err := sqReadAll(bytes.NewReader([]byte("123456789")), 8); err == nil {
		t.Error("flux trop long accepté")
	}
}

// Dossier ouvert (FOPEN_CACHE_DIR) : toutes les entrées, triées, puis
// retour au début (Seekdir 0) qui les rend à nouveau.
func TestImageOpendirHandle(t *testing.T) {
	sq := sqTestImage(t, "zstd", func(d string) {
		_ = os.MkdirAll(filepath.Join(d, "dossier"), 0o755)
		for _, n := range []string{"b.dll", "a.exe", "c.ini"} {
			_ = os.WriteFile(filepath.Join(d, "dossier", n), []byte(n), 0o644)
		}
	})
	n := sqTestNode(t, sq, "dossier")
	fh, flags, errno := n.OpendirHandle(context.Background(), 0)
	if errno != 0 || flags&fuse.FOPEN_CACHE_DIR == 0 {
		t.Fatalf("OpendirHandle : flags %#x, errno %v", flags, errno)
	}
	h := fh.(*sqDirHandle)
	names := func() []string {
		var out []string
		for {
			e, errno := h.Readdirent(context.Background())
			if errno != 0 {
				t.Fatalf("Readdirent : %v", errno)
			}
			if e == nil {
				return out
			}
			out = append(out, e.Name)
		}
	}
	want := "a.exe b.dll c.ini"
	if got := strings.Join(names(), " "); got != want {
		t.Errorf("entrées : %q, attendu %q", got, want)
	}
	if errno := h.Seekdir(context.Background(), 0); errno != 0 {
		t.Fatalf("Seekdir : %v", errno)
	}
	if got := strings.Join(names(), " "); got != want {
		t.Errorf("après Seekdir 0 : %q, attendu %q", got, want)
	}
	h.Releasedir(context.Background(), 0)
	if _, _, errno := sqTestNode(t, sq, "dossier").OpendirHandle(context.Background(), 0); errno != 0 {
		t.Errorf("réouverture : %v", errno)
	}
}
