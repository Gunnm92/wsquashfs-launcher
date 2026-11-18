package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"wsquashfs-launcher/internal/ui"
)

// TestRunOptionsWithoutDashes : les options de --setup s'écrivent sans tirets (comme --agent stop,
// --sync status) ; les anciennes formes (--yes, -y, --agent…) sont refusées sans rien installer.
func TestRunOptionsWithoutDashes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Cleanup(func() { assumeYes, dryRun = false, false })
	t.Cleanup(ui.SetOutput(&bytes.Buffer{}, &bytes.Buffer{}))
	for _, old := range []string{"--yes", "-y", "--dry-run", "-n", "--uninstall", "-u", "--help", "-h", "--agent", "agent"} {
		if code := Run([]string{old}, Env{}); code != 1 {
			t.Errorf("--setup %s : code %d, attendu 1 (option refusée)", old, code)
		}
	}
	if code := Run([]string{"help"}, Env{}); code != 0 {
		t.Errorf("--setup help : code %d", code)
	}
}

// installTree : une copie ratée laisse l'ancienne version en place.
func TestInstallTreeKeepsOldOnFailure(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "runner")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dst, "v"), []byte("ancien"), 0o644)
	if installTree(filepath.Join(dir, "absent"), dst) == nil {
		t.Fatal("copie d'une source absente acceptée")
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "v")); !bytes.Equal(data, []byte("ancien")) {
		t.Fatal("ancienne version perdue après un échec")
	}
	src := filepath.Join(dir, "src")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "v"), []byte("nouveau"), 0o644)
	if err := installTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "v")); !bytes.Equal(data, []byte("nouveau")) {
		t.Fatalf("contenu %q, attendu nouveau", data)
	}
	if pathExists(dst+".new") || pathExists(dst+".old") {
		t.Error("dossiers intermédiaires restés")
	}
}
