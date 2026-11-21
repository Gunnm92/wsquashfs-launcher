// Package contracttest : valide des requêtes et des réponses HTTP contre contract/agent-api.yaml,
// le contrat commun de l'agent et du manager (OpenAPI 3.1, schémas JSON Schema 2020-12).
//
// Paquet réservé aux tests : le lanceur ne l'importe pas, ses dépendances (santhosh-tekuri/jsonschema
// pour valider, yaml.v3 pour lire le contrat) ne sont jamais liées au binaire. Le même fichier est
// copié dans wsquashfs-manager (backend/contract/), dont les tests Python font la même validation.
package contracttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const resourceURL = "urn:wsq:agent-api"

// Contract : le contrat chargé et ses schémas compilés.
type Contract struct {
	doc      map[string]any
	compiler *jsonschema.Compiler
	cache    map[string]*jsonschema.Schema
}

// Path : chemin du contrat dans le dépôt du lanceur.
func Path() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "contract", "agent-api.yaml")
}

// CasesDir : dossier des tables de cas partagées (contract/cases/), copiées dans le manager.
func CasesDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "contract", "cases")
}

// Text : valeur d'un cas de table : une chaîne YAML, ou {repeat: "a", count: 200} (texte répété, pour
// les bornes de longueur). Le manager lit les mêmes tables (backend/tests/test_contract_cases.py).
type Text string

// UnmarshalYAML : chaîne, ou texte répété.
func (t *Text) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*t = Text(n.Value)
		return nil
	}
	var rep struct {
		Repeat string `yaml:"repeat"`
		Count  int    `yaml:"count"`
	}
	if err := n.Decode(&rep); err != nil {
		return err
	}
	*t = Text(strings.Repeat(rep.Repeat, rep.Count))
	return nil
}

// LoadCases : lit la table contract/cases/<name> dans out (YAML).
func LoadCases(t testing.TB, name string, out any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(CasesDir(), name))
	if err != nil {
		t.Fatalf("table de cas illisible : %v", err)
	}
	if err := yaml.Unmarshal(raw, out); err != nil {
		t.Fatalf("table de cas %s : YAML invalide : %v", name, err)
	}
}

// Load : lit et prépare le contrat.
func Load(t testing.TB) *Contract {
	t.Helper()
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("contrat illisible : %v", err)
	}
	var decoded any
	if err := yaml.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("contrat : YAML invalide : %v", err)
	}
	asJSON, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("contrat : conversion en JSON : %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(asJSON))
	if err != nil {
		t.Fatalf("contrat : %v", err)
	}
	root, ok := doc.(map[string]any)
	if !ok {
		t.Fatal("contrat : objet attendu à la racine")
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(resourceURL, doc); err != nil {
		t.Fatalf("contrat : %v", err)
	}
	return &Contract{doc: root, compiler: c, cache: map[string]*jsonschema.Schema{}}
}

// Version : info.version, la version de protocole.
func (c *Contract) Version() string {
	info, _ := c.doc["info"].(map[string]any)
	v, _ := info["version"].(string)
	return v
}

// Route : une opération du contrat.
type Route struct {
	Method, Path string
	Op           map[string]any
}

// ServedBy : serveurs de la route (x-served-by : agent, manager).
func (r Route) ServedBy() []string {
	var out []string
	list, _ := r.Op["x-served-by"].([]any)
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Since : x-since-agent.
func (r Route) Since() int {
	n, _ := r.Op["x-since-agent"].(json.Number)
	v, _ := strconv.Atoi(n.String())
	return v
}

// Routes : toutes les opérations, triées.
func (c *Contract) Routes() []Route {
	var out []Route
	paths, _ := c.doc["paths"].(map[string]any)
	for p, item := range paths {
		methods, _ := item.(map[string]any)
		for m, op := range methods {
			if o, ok := op.(map[string]any); ok {
				out = append(out, Route{Method: strings.ToUpper(m), Path: p, Op: o})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

func (c *Contract) route(t testing.TB, method, path string) Route {
	t.Helper()
	for _, r := range c.Routes() {
		if r.Method == method && r.Path == path {
			return r
		}
	}
	t.Fatalf("route absente du contrat : %s %s", method, path)
	return Route{}
}

// pointer : segment de JSON Pointer échappé.
func pointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// resolve : suit un $ref local ("#/components/...") jusqu'à l'objet visé, et rend son pointeur.
func (c *Contract) resolve(node any, at string) (map[string]any, string) {
	for {
		m, _ := node.(map[string]any)
		ref, ok := m["$ref"].(string)
		if !ok {
			return m, at
		}
		at = strings.TrimPrefix(ref, "#")
		node = c.lookup(at)
	}
}

func (c *Contract) lookup(ptr string) any {
	var node any = c.doc
	for _, seg := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		m, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = m[seg]
	}
	return node
}

// schema : schéma compilé à l'emplacement (pointeur) donné.
func (c *Contract) schema(t testing.TB, ptr string) *jsonschema.Schema {
	t.Helper()
	if s, ok := c.cache[ptr]; ok {
		return s
	}
	s, err := c.compiler.Compile(resourceURL + "#" + ptr)
	if err != nil {
		t.Fatalf("contrat : schéma %s : %v", ptr, err)
	}
	c.cache[ptr] = s
	return s
}

// validate : valide un document JSON (octets) contre un schéma.
func (c *Contract) validate(t testing.TB, ptr string, body []byte, what string) {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Errorf("%s : JSON illisible : %v\n%s", what, err, body)
		return
	}
	if err := c.schema(t, ptr).Validate(inst); err != nil {
		t.Errorf("%s : hors contrat :\n%v\ncorps : %s", what, err, body)
	}
}

// ValidateSchema : valide des octets JSON contre un schéma nommé de components/schemas.
func (c *Contract) ValidateSchema(t testing.TB, name string, body []byte) {
	t.Helper()
	c.validate(t, "/components/schemas/"+name, body, "schéma "+name)
}

// CheckResponse : la réponse (code, en-têtes, corps) d'une route doit être dans le contrat : code
// déclaré, type de contenu déclaré, en-têtes obligatoires présents, corps JSON conforme au schéma.
// pair : réponse du manager, validée avec x-pair-schema quand la route en a un. Un 404 ou 405 en texte
// brut (route inconnue du routeur) est toujours admis.
func (c *Contract) CheckResponse(t testing.TB, method, target string, status int, header http.Header, body []byte, pair bool) {
	t.Helper()
	path := strings.SplitN(target, "?", 2)[0]
	r := c.route(t, method, path)
	what := fmt.Sprintf("%s %s -> %d", method, path, status)
	ctype := strings.ToLower(strings.TrimSpace(strings.SplitN(header.Get("Content-Type"), ";", 2)[0]))
	if (status == 404 || status == 405) && ctype == "text/plain" {
		return
	}
	responses, _ := r.Op["responses"].(map[string]any)
	node, ok := responses[strconv.Itoa(status)]
	if !ok {
		node, ok = responses["default"]
	}
	if !ok {
		t.Errorf("%s : code absent du contrat", what)
		return
	}
	resp, at := c.resolve(node, "/paths/"+pointer(path)+"/"+strings.ToLower(method)+"/responses/"+strconv.Itoa(status))
	if hdrs, ok := resp["headers"].(map[string]any); ok {
		for name, h := range hdrs {
			hm, _ := h.(map[string]any)
			if req, _ := hm["required"].(bool); req && header.Get(name) == "" {
				t.Errorf("%s : en-tête %s absent", what, name)
			}
		}
	}
	content, _ := resp["content"].(map[string]any)
	if len(content) == 0 {
		return
	}
	media, ok := content[ctype].(map[string]any)
	if !ok {
		t.Errorf("%s : type de contenu %q absent du contrat", what, ctype)
		return
	}
	if ctype != "application/json" {
		return
	}
	key := "schema"
	if _, has := media["x-pair-schema"]; has && pair {
		key = "x-pair-schema"
	}
	c.validate(t, at+"/content/"+pointer(ctype)+"/"+key, body, what)
}

// CheckRecorder : CheckResponse pour la réponse d'un handler testé en mémoire.
func (c *Contract) CheckRecorder(t testing.TB, method, target string, status int, header http.Header, body []byte) {
	t.Helper()
	c.CheckResponse(t, method, target, status, header, body, false)
}

// CheckRequest : une requête (méthode, chemin avec requête, en-têtes, corps) doit être dans le
// contrat : route connue, paramètres de requête déclarés (et les obligatoires présents), en-têtes
// déclarés conformes à leur schéma (les obligatoires présents), corps JSON conforme au schéma.
func (c *Contract) CheckRequest(t testing.TB, method, target string, header http.Header, body []byte) {
	t.Helper()
	path, rawQuery, _ := strings.Cut(target, "?")
	r := c.route(t, method, path)
	what := fmt.Sprintf("requête %s %s", method, path)
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Errorf("%s : requête illisible : %v", what, err)
		return
	}
	declared := map[string]bool{}
	params, _ := r.Op["parameters"].([]any)
	for i, p := range params {
		pm, pat := c.resolve(p, "/paths/"+pointer(path)+"/"+strings.ToLower(method)+"/parameters/"+strconv.Itoa(i))
		name, _ := pm["name"].(string)
		in, _ := pm["in"].(string)
		required, _ := pm["required"].(bool)
		var val string
		var present bool
		switch in {
		case "query":
			declared[name] = true
			val = query.Get(name)
			present = query.Has(name)
		case "header":
			val = header.Get(name)
			present = len(header.Values(name)) > 0
		}
		if !present {
			if required {
				t.Errorf("%s : %s %s obligatoire absent", what, in, name)
			}
			continue
		}
		if sch, ok := pm["schema"]; ok {
			_ = sch
			enc, _ := json.Marshal(val)
			c.validate(t, pat+"/schema", enc, fmt.Sprintf("%s : %s %s", what, in, name))
		}
	}
	for name := range query {
		if !declared[name] {
			t.Errorf("%s : paramètre de requête %q absent du contrat", what, name)
		}
	}
	rb, _ := r.Op["requestBody"].(map[string]any)
	if rb == nil {
		if len(body) > 0 {
			t.Errorf("%s : corps envoyé, le contrat n'en décrit pas", what)
		}
		return
	}
	content, _ := rb["content"].(map[string]any)
	if _, ok := content["application/json"]; ok && json.Valid(body) {
		c.validate(t, "/paths/"+pointer(path)+"/"+strings.ToLower(method)+"/requestBody/content/application~1json/schema", body, what+" : corps")
	}
	if _, ok := content["application/json"]; ok && !json.Valid(body) {
		t.Errorf("%s : corps JSON attendu : %q", what, body)
	}
}
