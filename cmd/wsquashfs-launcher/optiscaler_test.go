package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestOptiscalerDllName(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"", "", true}, {"0", "", true}, {"1", "dxgi", true},
		{"winmm", "winmm", true}, {"Version.dll", "version", true},
		{"d3d11", "", false}, {"oui", "", false},
	}
	for _, c := range cases {
		got, ok := optiscalerDllName(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("optiscalerDllName(%q) = %q, %v ; attendu %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// optiscalerFixture : OptiScaler installé dans HOME, jeu dans un prefix avec
// son propre libxess.dll.
func optiscalerFixture(t *testing.T) (gameDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := optiscalerBase()
	for _, f := range []string{"OptiScaler.dll", "OptiScaler.ini", "libxess.dll", "amd_fidelityfx_dx12.dll",
		"fakenvapi.dll", "fakenvapi.ini", "setup_linux.sh", "!! README !!.txt", "VERSION", "D3D12_Optiscaler/D3D12Core.dll", "Licenses/x.txt"} {
		p := filepath.Join(src, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prefix := t.TempDir()
	gameDir = filepath.Join(prefix, "game", "bin")
	_ = os.MkdirAll(gameDir, 0o755)
	_ = os.WriteFile(filepath.Join(gameDir, "Game.exe"), []byte("exe"), 0o644)
	_ = os.WriteFile(filepath.Join(gameDir, "libxess.dll"), []byte("du jeu"), 0o644)
	saved := launch
	t.Cleanup(func() { launch = saved })
	launch.winePoint = prefix
	launch.execPath = filepath.Join(gameDir, "Game.exe")
	launch.hasNvidia = "1"
	return gameDir
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestInstallOptiscalerLinksAndRemoves(t *testing.T) {
	dir := optiscalerFixture(t)
	launch.optiscaler = "1"
	resolveOptiscaler()
	if launch.optiscalerDll != "dxgi" {
		t.Fatalf("optiscalerDll = %q", launch.optiscalerDll)
	}
	kept := installOptiscaler()
	if !slices.Equal(kept, []string{"libxess.dll"}) {
		t.Errorf("fichiers du jeu gardés = %v", kept)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "libxess.dll")); string(data) != "du jeu" {
		t.Errorf("libxess.dll du jeu remplacé : %q", data)
	}
	if !isOptiscalerLink(filepath.Join(dir, "dxgi.dll")) || !isOptiscalerLink(filepath.Join(dir, "D3D12_Optiscaler")) {
		t.Errorf("liens absents : %v", dirNames(t, dir))
	}
	// NVIDIA : pas de fakenvapi ; jamais la doc, les scripts ni VERSION.
	for _, f := range []string{"fakenvapi.dll", "fakenvapi.ini", "setup_linux.sh", "!! README !!.txt", "VERSION", "Licenses", "OptiScaler.dll"} {
		if pathExists(filepath.Join(dir, f)) {
			t.Errorf("%s ne devait pas être relié", f)
		}
	}
	ini := filepath.Join(dir, "OptiScaler.ini")
	if st, err := os.Lstat(ini); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("OptiScaler.ini non copié (fichier ordinaire attendu)")
	}
	_ = os.WriteFile(ini, []byte("réglé"), 0o644)

	// Autre nom : l'ancien lien part, le réglage reste.
	launch.optiscaler = "winmm"
	resolveOptiscaler()
	installOptiscaler()
	if pathExists(filepath.Join(dir, "dxgi.dll")) || !isOptiscalerLink(filepath.Join(dir, "winmm.dll")) {
		t.Errorf("changement de nom : %v", dirNames(t, dir))
	}
	if data, _ := os.ReadFile(ini); string(data) != "réglé" {
		t.Errorf("OptiScaler.ini écrasé : %q", data)
	}

	// Coupé : plus aucun lien, fichiers du jeu et réglages intacts.
	launch.optiscaler = "0"
	resolveOptiscaler()
	installOptiscaler()
	got := dirNames(t, dir)
	want := []string{"Game.exe", "OptiScaler.ini", "libxess.dll"}
	if !slices.Equal(got, want) {
		t.Errorf("après désactivation : %v ; attendu %v", got, want)
	}
}

// Lien posé sous un autre HOME (/home/arcade puis /config) : reconnu et retiré.
func TestOptiscalerLinkOtherHome(t *testing.T) {
	dir := optiscalerFixture(t)
	stale := filepath.Join(dir, "dxgi.dll")
	if err := os.Symlink("/autre/home/.local/share/wsquashfs/optiscaler/OptiScaler.dll", stale); err != nil {
		t.Fatal(err)
	}
	launch.optiscaler = "1"
	resolveOptiscaler()
	if launch.optiscalerDll != "dxgi" {
		t.Fatalf("ancien lien pris pour le dxgi.dll du jeu")
	}
	installOptiscaler()
	if got, _ := os.Readlink(stale); got != filepath.Join(optiscalerBase(), "OptiScaler.dll") {
		t.Errorf("lien non remplacé : %q", got)
	}
}

func TestOptiscalerFakenvapiWithoutNvidia(t *testing.T) {
	dir := optiscalerFixture(t)
	launch.hasNvidia = "0"
	launch.optiscaler = "1"
	resolveOptiscaler()
	installOptiscaler()
	if !isOptiscalerLink(filepath.Join(dir, "fakenvapi.dll")) {
		t.Errorf("fakenvapi.dll attendu sans carte NVIDIA : %v", dirNames(t, dir))
	}
}

func TestOptiscalerGameOwnDll(t *testing.T) {
	dir := optiscalerFixture(t)
	_ = os.WriteFile(filepath.Join(dir, "dxgi.dll"), []byte("ReShade"), 0o644)
	launch.optiscaler = "1"
	resolveOptiscaler()
	if launch.optiscalerDll != "" {
		t.Fatalf("dxgi.dll du jeu présent : OptiScaler aurait dû être écarté")
	}
	installOptiscaler()
	if data, _ := os.ReadFile(filepath.Join(dir, "dxgi.dll")); string(data) != "ReShade" {
		t.Errorf("dxgi.dll du jeu touché : %q", data)
	}
}

func TestMergeIniOverrides(t *testing.T) {
	base := "; OptiScaler\n[Dxgi]\nFps=60\n; commentaire gardé\nHook=true\n\n[Graphics]\nMode=auto\n"
	cas := []struct {
		nom, base, modele, want string
	}{
		{"remplacement", base, "[Dxgi]\nFps = 144\n", "; OptiScaler\n[Dxgi]\nFps=144\n; commentaire gardé\nHook=true\n\n[Graphics]\nMode=auto\n"},
		{"ajout dans la section", base, "[Dxgi]\nNew=1\n", "; OptiScaler\n[Dxgi]\nFps=60\n; commentaire gardé\nHook=true\nNew=1\n\n[Graphics]\nMode=auto\n"},
		{"nouvelle section", base, "[Autre]\nA=b\n", base + "\n[Autre]\nA=b\n"},
		{"CRLF gardé", strings.ReplaceAll(base, "\n", "\r\n"), "[Graphics]\r\nMode=quality\r\n", strings.ReplaceAll("; OptiScaler\n[Dxgi]\nFps=60\n; commentaire gardé\nHook=true\n\n[Graphics]\nMode=quality\n", "\n", "\r\n")},
		{"commentaires du modèle ignorés", base, "# note\n; autre\n\n[Dxgi]\n# Hook=false\nFps=30\n", "; OptiScaler\n[Dxgi]\nFps=30\n; commentaire gardé\nHook=true\n\n[Graphics]\nMode=auto\n"},
		{"clé d'une autre section ne touche pas", base, "[Graphics]\nFps=30\n", "; OptiScaler\n[Dxgi]\nFps=60\n; commentaire gardé\nHook=true\n\n[Graphics]\nMode=auto\nFps=30\n"},
		{"base vide", "", "[A]\nk=v\n", "[A]\nk=v\n"},
	}
	for _, c := range cas {
		if got := mergeIniOverrides(c.base, c.modele); got != c.want {
			t.Errorf("%s :\n got %q\nwant %q", c.nom, got, c.want)
		}
	}
}

// Modèle machine puis modèle dossier : le dossier l'emporte ; ini déjà présent non touché.
func TestOptiscalerModelesMachineDossier(t *testing.T) {
	dir := optiscalerFixture(t)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "conf"))
	saved := launch.wsquashfsFile
	t.Cleanup(func() { launch.wsquashfsFile = saved })
	launch.wsquashfsFile = filepath.Join(root, "jeu.wsquashfs")
	machine := filepath.Join(root, "conf", "wsquashfs", "optiscaler.ini")
	dossier := filepath.Join(root, "_optiscaler.ini")
	_ = os.MkdirAll(filepath.Dir(machine), 0o755)
	_ = os.WriteFile(machine, []byte("[Dxgi]\nFps=30\nHook=false\n"), 0o644)
	_ = os.WriteFile(dossier, []byte("[Dxgi]\nFps=144\n"), 0o644)

	launch.optiscaler = "1"
	resolveOptiscaler()
	installOptiscaler()
	data, _ := os.ReadFile(filepath.Join(dir, "OptiScaler.ini"))
	if !strings.Contains(string(data), "Fps=144") || strings.Contains(string(data), "Fps=30") || !strings.Contains(string(data), "Hook=false") {
		t.Errorf("le dossier doit l'emporter sur la machine : %q", data)
	}
	if !slices.Equal(launch.optiscalerModeles, []string{"modèle machine", "modèle dossier"}) {
		t.Errorf("modèles appliqués = %v", launch.optiscalerModeles)
	}

	// Ini déjà présent : aucun modèle appliqué, contenu intact.
	_ = os.WriteFile(filepath.Join(dir, "OptiScaler.ini"), []byte("réglé"), 0o644)
	launch.optiscalerModeles = nil
	installOptiscaler()
	if got, _ := os.ReadFile(filepath.Join(dir, "OptiScaler.ini")); string(got) != "réglé" || launch.optiscalerModeles != nil {
		t.Errorf("ini existant touché : %q, modèles %v", got, launch.optiscalerModeles)
	}
}
