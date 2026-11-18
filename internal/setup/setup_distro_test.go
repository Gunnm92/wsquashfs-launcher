package setup

import (
	"os"
	"path/filepath"
	"testing"
)

// SteamOS (ID_LIKE=arch) a son propre cas : racine en lecture seule, pas de
// pacman -S (Steam Deck, 16/11).
func TestDetectDistroSteamOS(t *testing.T) {
	for content, want := range map[string]string{
		"ID=steamos\nID_LIKE=arch\nPRETTY_NAME=\"SteamOS\"\n": "steamos",
		"ID=arch\n":                          "arch",
		"ID=cachyos\nID_LIKE=arch\n":         "arch",
		"ID=ubuntu\nID_LIKE=debian\n":        "debian",
		"ID=linuxmint\nID_LIKE=\"ubuntu\"\n": "debian",
	} {
		f := filepath.Join(t.TempDir(), "os-release")
		if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OS_RELEASE", f)
		detectDistro()
		if distro != want {
			t.Errorf("%q : %s, %s attendu", content, distro, want)
		}
	}
}
