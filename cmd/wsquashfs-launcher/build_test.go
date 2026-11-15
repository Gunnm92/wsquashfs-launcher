package main

import "testing"

// TestShortBuild : la version affichée est les 12 premiers caractères de l'empreinte, et une
// empreinte plus courte (ou vide) est affichée telle quelle.
func TestShortBuild(t *testing.T) {
	full := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got := shortBuild(full); got != "0123456789ab" {
		t.Errorf("shortBuild = %q, attendu 0123456789ab", got)
	}
	if got := shortBuild("abc"); got != "abc" {
		t.Errorf("shortBuild(abc) = %q", got)
	}
	if got := shortBuild(""); got != "" {
		t.Errorf("shortBuild(vide) = %q", got)
	}
}

// TestLauncherBuildIsShortHash : launcherBuild rend 12 caractères hexadécimaux (ou « inconnue »).
func TestLauncherBuildIsShortHash(t *testing.T) {
	got := launcherBuild()
	if got == "inconnue" {
		t.Skip("binaire de test illisible")
	}
	if len(got) != 12 {
		t.Fatalf("launcherBuild = %q, 12 caractères attendus", got)
	}
	for _, c := range got {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("launcherBuild = %q, hexadécimal attendu", got)
		}
	}
}
