package main

// Conversion des sauvegardes au format de l'overlay noyau (contrôle équivalent
// à l'ancien test bash « saves_to_kernel_format »). Ignoré sans setfattr ni
// attributs étendus utilisateur.

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSavesToKernelFormat(t *testing.T) {
	for _, tool := range []string{"setfattr", "getfattr", "mknod"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s absent", tool)
		}
	}
	u := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(u, "opq"), 0o755))
	must(os.MkdirAll(filepath.Join(u, "Développé\\x"), 0o755))
	must(os.MkdirAll(filepath.Join(u, "wh"), 0o755))
	must(os.WriteFile(filepath.Join(u, "opq", ".wh..wh..opq"), nil, 0o644))
	if exec.Command("setfattr", "-n", "user.fuseoverlayfs.opaque", "-v", "y", filepath.Join(u, "Développé\\x")).Run() != nil {
		t.Skip("attributs étendus utilisateur indisponibles")
	}
	must(os.WriteFile(filepath.Join(u, "wh", ".wh.gone"), nil, 0o644))
	must(os.WriteFile(filepath.Join(u, "wh", ".wh.kept"), nil, 0o644))
	must(os.WriteFile(filepath.Join(u, "wh", "kept"), []byte("k\n"), 0o644))

	if !savesToKernelFormat(u) {
		t.Fatal("conversion refusée")
	}

	// Dossiers opaques : attribut de l'overlay noyau.
	for _, d := range []string{"opq", "Développé\\x"} {
		out, _ := exec.Command("getfattr", "--only-values", "-n", "user.overlay.opaque", filepath.Join(u, d)).Output()
		if string(out) != "y" {
			t.Errorf("%s : user.overlay.opaque = %q, attendu y", d, out)
		}
	}
	if out, _ := exec.Command("getfattr", "--only-values", "-n", "user.overlay.opaque", filepath.Join(u, "wh")).Output(); len(out) != 0 {
		t.Errorf("wh : attribut opaque inattendu %q", out)
	}
	// .wh..wh..opq retiré.
	if _, err := os.Lstat(filepath.Join(u, "opq", ".wh..wh..opq")); err == nil {
		t.Error(".wh..wh..opq encore présent")
	}
	// Fichier supprimé (.wh.gone) : périphérique caractère 0/0 à la place.
	st, err := os.Lstat(filepath.Join(u, "wh", "gone"))
	if err != nil {
		t.Fatalf("wh/gone absent : %v", err)
	}
	if st.Mode()&os.ModeCharDevice == 0 || st.Sys().(*syscall.Stat_t).Rdev != 0 {
		t.Errorf("wh/gone : périphérique 0/0 attendu, mode %v", st.Mode())
	}
	// Fichier existant : gardé tel quel, son whiteout retiré.
	if data, _ := os.ReadFile(filepath.Join(u, "wh", "kept")); string(data) != "k\n" {
		t.Errorf("wh/kept modifié : %q", data)
	}
	for _, w := range []string{".wh.gone", ".wh.kept"} {
		if _, err := os.Lstat(filepath.Join(u, "wh", w)); err == nil {
			t.Errorf("%s encore présent", w)
		}
	}
}
