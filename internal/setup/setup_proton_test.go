package setup

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCpuHasX86_64V3 : archive proton-cachyos x86_64_v3 seulement si le
// processeur a toutes les instructions du niveau (20/11).
func TestCpuHasX86_64V3(t *testing.T) {
	v3 := "processor\t: 0\nflags\t\t: fpu sse2 avx avx2 bmi1 bmi2 f16c fma abm movbe xsave sse4_2\n"
	if !cpuHasX86_64V3(v3) {
		t.Error("processeur x86-64-v3 non reconnu")
	}
	noAVX2 := "processor\t: 0\nflags\t\t: fpu sse2 avx bmi1 f16c fma abm movbe xsave sse4_2\n"
	if cpuHasX86_64V3(noAVX2) {
		t.Error("processeur sans AVX2 pris pour x86-64-v3")
	}
	if cpuHasX86_64V3("processor\t: 0\n") {
		t.Error("cpuinfo sans drapeaux pris pour x86-64-v3")
	}
}

// TestFindProtonPrefersCachyos : proton-cachyos passe avant UMU-Proton, où
// qu'il soit installé (20/11 : dans compatibilitytools.d de l'utilisateur).
func TestFindProtonPrefersCachyos(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	old := protonInstallDir
	protonInstallDir = filepath.Join(home, ".local/share/Steam/compatibilitytools.d")
	t.Cleanup(func() { protonInstallDir = old })
	mk := func(name string) string {
		d := filepath.Join(protonInstallDir, name)
		for _, f := range []string{"proton", "files/bin/wine"} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(d, f)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, f), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	umu := mk("UMU-Proton-10.0-4")
	if got := findProton(); got != umu {
		t.Fatalf("UMU-Proton seul : %q", got)
	}
	if got := findProtonMatching("proton-cachyos*"); got != "" {
		t.Fatalf("proton-cachyos trouvé sans être installé : %q", got)
	}
	cachy := mk("proton-cachyos-11.0-20261015-slr-x86_64_v3")
	if got := findProton(); got != cachy {
		t.Fatalf("proton-cachyos attendu en premier, obtenu %q", got)
	}
}
