package main

// Sorties de référence relevées sur le script bash (source + appel direct).

import (
	"strings"
	"testing"
)

func TestRunnerName(t *testing.T) {
	cases := map[string]string{
		"":                               "aucun Wine",
		"/usr/local/bin/proton":          "proton (/usr/local/bin/proton)",
		"/usr/bin/wine64":                "wine64 (/usr/bin/wine64)",
		"/x/wine-tkg/bin/wine":           "wine-tkg",
		"/x/GE-Proton9-1/files/bin/wine": "GE-Proton9-1",
	}
	for in, want := range cases {
		if got := runnerName(in); got != want {
			t.Errorf("runnerName(%q) = %q, attendu %q", in, got, want)
		}
	}
}

func TestSplitCommandLine(t *testing.T) {
	cases := map[string]string{
		"-a -b":                   "[-a] [-b]",
		`-path "C:\Mon Jeu\x" -n`: `[-path] [C:\Mon Jeu\x] [-n]`,
		`"" fin`:                  "[] [fin]",
		"  x   y  ":               "[x] [y]",
		`a"b c"d`:                 "[ab cd]",
	}
	for in, want := range cases {
		var b strings.Builder
		for _, a := range splitCommandLine(in) {
			b.WriteString("[" + a + "]")
			b.WriteString(" ")
		}
		got := strings.TrimSpace(strings.ReplaceAll(b.String(), "] [", "] ["))
		if got != want {
			t.Errorf("splitCommandLine(%q) = %q, attendu %q", in, got, want)
		}
	}
}

func TestRegexEscape(t *testing.T) {
	cases := map[string]string{
		"a.b*c": `a\.b\*c`,
		"x[y]z": `x\[y\]z`,
		`p\q`:   `p\\q`,
		"a/b":   `a\/b`,
	}
	for in, want := range cases {
		if got := regexEscape(in); got != want {
			t.Errorf("regexEscape(%q) = %q, attendu %q", in, got, want)
		}
	}
}

func TestUnquote(t *testing.T) {
	cases := map[string]string{`"quoted"`: "quoted", "'single'": "single", "plain": "plain", `"half`: `"half`}
	for in, want := range cases {
		if got := unquote(in); got != want {
			t.Errorf("unquote(%q) = %q, attendu %q", in, got, want)
		}
	}
}

func TestTextWidthAndPad(t *testing.T) {
	if w := textWidth("Développé"); w != 9 {
		t.Errorf("textWidth = %d, attendu 9", w)
	}
	if p := pad("Développé", 12); p != "Développé   " {
		t.Errorf("pad = %q", p)
	}
}
