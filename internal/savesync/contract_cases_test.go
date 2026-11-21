package savesync

// Tables de cas partagées avec le manager (contract/cases/) : les mêmes entrées, les mêmes résultats
// attendus des deux côtés. Les tests Python (backend/tests/test_contract_cases.py) lisent les mêmes
// fichiers.

import (
	"testing"

	"wsquashfs-launcher/internal/contracttest"
)

// caseSide : une machine d'un cas (client ou manager) : empreinte, date du plus récent, contenu.
type caseSide struct {
	Fingerprint string `yaml:"fingerprint"`
	Newest      int64  `yaml:"newest"`
	Content     string `yaml:"content"`
}

type syncCase struct {
	Name    string   `yaml:"name"`
	Base    string   `yaml:"base"`
	Client  caseSide `yaml:"client"`
	Manager caseSide `yaml:"manager"`
	Expect  struct {
		Go      string `yaml:"go"`
		Manager string `yaml:"manager"`
	} `yaml:"expect"`
}

// goActions : vocabulaire des cas (sync-decisions.yaml) vers syncAction ; la correspondance avec le
// vocabulaire du manager est dans l'en-tête de la table.
var goActions = map[string]syncAction{
	"à jour":    syncUpToDate,
	"envoi":     syncSend,
	"réception": syncReceive,
	"conflit":   syncConflict,
}

// TestContractSyncDecisions : decideSync rend, pour chaque cas, l'action attendue (base commune aux
// deux côtés).
func TestContractSyncDecisions(t *testing.T) {
	var cases struct {
		Cases []syncCase `yaml:"cases"`
	}
	contracttest.LoadCases(t, "sync-decisions.yaml", &cases)
	if len(cases.Cases) < 20 {
		t.Fatalf("table trop courte : %d cas", len(cases.Cases))
	}
	for _, c := range cases.Cases {
		want, ok := goActions[c.Expect.Go]
		if !ok {
			t.Errorf("%s : action inconnue dans la table : %q", c.Name, c.Expect.Go)
			continue
		}
		got := decideSync(c.Client.Fingerprint, c.Base, c.Client.Newest,
			c.Manager.Fingerprint, c.Base, c.Manager.Newest, c.Client.Content, c.Manager.Content)
		if got != want {
			t.Errorf("%s : decideSync = %v, attendu %v (%q)", c.Name, got, want, c.Expect.Go)
		}
	}
}

// TestContractNames : noms de jeu, identifiants de machine et chemins de dossiers déclarés.
func TestContractNames(t *testing.T) {
	type list struct {
		Valid   []contracttest.Text `yaml:"valid"`
		Invalid []contracttest.Text `yaml:"invalid"`
	}
	var table struct {
		Games        list `yaml:"games"`
		Machines     list `yaml:"machines"`
		DossierPaths list `yaml:"dossier_paths"`
	}
	contracttest.LoadCases(t, "names.yaml", &table)
	check := func(what string, l list, ok func(string) bool) {
		t.Helper()
		for _, v := range l.Valid {
			if !ok(string(v)) {
				t.Errorf("%s : %q refusé, attendu valide", what, string(v))
			}
		}
		for _, v := range l.Invalid {
			if ok(string(v)) {
				t.Errorf("%s : %q accepté, attendu invalide", what, string(v))
			}
		}
	}
	check("jeu", table.Games, ValidGame)
	check("identifiant de machine", table.Machines, ValidID)
	check("dossier déclaré", table.DossierPaths, func(p string) bool { return checkDeclaredRel(p) == nil })
}

// TestContractLimits : taille maximale d'archive et format d'empreinte.
func TestContractLimits(t *testing.T) {
	var table struct {
		ArchiveMaxBytes int64 `yaml:"archive_max_bytes"`
		Fingerprint     struct {
			Pattern string   `yaml:"pattern"`
			Valid   []string `yaml:"valid"`
			Invalid []string `yaml:"invalid"`
		} `yaml:"fingerprint"`
	}
	contracttest.LoadCases(t, "limits.yaml", &table)
	if DefaultMaxBody != table.ArchiveMaxBytes {
		t.Errorf("DefaultMaxBody = %d, attendu %d (limits.yaml)", DefaultMaxBody, table.ArchiveMaxBytes)
	}
	if want := "^" + table.Fingerprint.Pattern + "$"; pairFPRe.String() != want {
		t.Errorf("pairFPRe = %q, attendu %q", pairFPRe.String(), want)
	}
	for _, fp := range table.Fingerprint.Valid {
		if !ValidFingerprint(fp) {
			t.Errorf("empreinte %q refusée, attendue valide", fp)
		}
	}
	for _, fp := range table.Fingerprint.Invalid {
		if ValidFingerprint(fp) {
			t.Errorf("empreinte %q acceptée, attendue invalide", fp)
		}
	}
}
