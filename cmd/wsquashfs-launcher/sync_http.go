package main

// Client de la synchro (--sync) : parle aux routes /v1/saves/* d'une autre machine (agent_http.go).
// La machine qui lance --sync fait tout : elle lit la liste distante, puis reçoit ou envoie.
// Le certificat distant est épinglé (empreinte du code d'association), pas la chaîne de confiance.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// syncPeer : une machine distante, telle qu'elle est enregistrée dans sync.json.
type syncPeer struct {
	Name        string `json:"name"`
	URL         string `json:"url"`   // https://hôte:port
	Token       string `json:"token"` // jeton durable de cette machine chez l'autre (jamais le secret du code)
	Fingerprint string `json:"fingerprint"`
}

// syncConfig : sync.json (identifiant de cette machine et machines associées).
type syncConfig struct {
	ID    string     `json:"id"`
	Peers []syncPeer `json:"peers"`
}

// syncConfigPath : ~/.config/wsquashfs/sync.json (XDG_CONFIG_HOME respecté).
func syncConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(base, "wsquashfs", "sync.json")
}

// newSyncID : identifiant aléatoire de cette machine (16 octets, hexadécimal).
func newSyncID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic("identifiant de synchro impossible : " + err.Error())
	}
	return hex.EncodeToString(buf)
}

// loadSyncConfig : sync.json ; identifiant créé s'il manque (sans écrire le fichier).
func loadSyncConfig() syncConfig {
	var cfg syncConfig
	if data, err := os.ReadFile(syncConfigPath()); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if !validSyncID(cfg.ID) {
		cfg.ID = newSyncID()
	}
	return cfg
}

// saveSyncConfig : écriture atomique, droits 600 (dossier 700).
func saveSyncConfig(cfg syncConfig) error {
	path := syncConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

var (
	pairTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	pairFPRe    = regexp.MustCompile(`^[0-9A-Fa-f]{64}$`)
)

// parsePairCode : code d'association wsq://JETON@HÔTE:PORT#EMPREINTE (sortie de --pair).
// Le nom de la machine n'est pas dans le code : il reste vide.
func parsePairCode(code string) (syncPeer, error) {
	const scheme = "wsq://"
	if !strings.HasPrefix(code, scheme) {
		return syncPeer{}, errors.New("code d'association : il doit commencer par wsq://")
	}
	rest, fp, ok := strings.Cut(code[len(scheme):], "#")
	if !ok {
		return syncPeer{}, errors.New("code d'association : empreinte (#…) absente")
	}
	token, hostPort, ok := strings.Cut(rest, "@")
	if !ok || token == "" {
		return syncPeer{}, errors.New("code d'association : jeton (wsq://JETON@…) absent")
	}
	if !pairTokenRe.MatchString(token) {
		return syncPeer{}, errors.New("code d'association : jeton mal formé")
	}
	host, portStr, err := net.SplitHostPort(hostPort)
	if err != nil || host == "" {
		return syncPeer{}, errors.New("code d'association : hôte:port attendu")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return syncPeer{}, errors.New("code d'association : port invalide")
	}
	if !pairFPRe.MatchString(fp) {
		return syncPeer{}, errors.New("code d'association : empreinte invalide (64 caractères hexadécimaux)")
	}
	return syncPeer{
		URL:         "https://" + net.JoinHostPort(host, portStr),
		Token:       token,
		Fingerprint: strings.ToUpper(fp),
	}, nil
}

// errSyncBusy : un jeu est en cours d'utilisation sur la machine distante (409).
var errSyncBusy = errors.New("jeu en cours sur la machine distante")

// errCertPin : le certificat distant n'a pas l'empreinte du code d'association.
var errCertPin = errors.New("empreinte du certificat différente")

// unreachableError : la machine distante ne répond pas (réseau, délai, serveur arrêté).
type unreachableError struct{ err error }

func (e *unreachableError) Error() string { return "machine injoignable : " + e.err.Error() }
func (e *unreachableError) Unwrap() error { return e.err }

// isUnreachable : vrai si la machine distante est injoignable (et non refusante).
func isUnreachable(err error) bool {
	var u *unreachableError
	return errors.As(err, &u)
}

// syncClient : client HTTPS d'une machine distante ; id est celui de cette machine.
type syncClient struct {
	peer syncPeer
	id   string
	http *http.Client
	slow *http.Client // réception d'une archive : en-têtes attendus plus longtemps (syncFetchHeaderTimeout)
}

// syncHeaderTimeout : attente des en-têtes d'une réponse ; au-delà, la machine est dite
// injoignable. Variable pour les tests.
var syncHeaderTimeout = 30 * time.Second

// syncFetchHeaderTimeout : attente des en-têtes pour la seule réception d'une archive
// (GET /v1/saves/tar) : l'agent écrit toute l'archive avant de répondre, une grosse sauvegarde
// est longue à compresser. Variable pour les tests.
var syncFetchHeaderTimeout = 10 * time.Minute

// remoteSave : une entrée de /v1/saves/list (côté client).
// (Le type est partagé avec la route : voir agent_http.go.)

// newSyncClient : client pour une machine distante. TLS 1.3 ; le certificat est accepté si son
// empreinte SHA-256 (DER) est celle du code d'association.
func newSyncClient(peer syncPeer, id string) *syncClient {
	want := strings.ToUpper(peer.Fingerprint)
	tr := &http.Transport{
		Proxy:                 nil, // réseau local : pas de proxy d'environnement
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: syncHeaderTimeout,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
			// La chaîne de confiance n'existe pas (certificat auto-signé) : c'est l'empreinte
			// épinglée qui fait foi, dans VerifyPeerCertificate.
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errCertPin
				}
				sum := sha256.Sum256(raw[0])
				if strings.ToUpper(hex.EncodeToString(sum[:])) != want {
					return errCertPin
				}
				return nil
			},
		},
	}
	// Même transport, seul le délai des en-têtes change.
	slow := tr.Clone()
	slow.ResponseHeaderTimeout = syncFetchHeaderTimeout
	return &syncClient{peer: peer, id: id, http: &http.Client{Transport: tr}, slow: &http.Client{Transport: slow}}
}

// label : nom de la machine pour les messages (le nom, à défaut l'adresse).
func (c *syncClient) label() string {
	if c.peer.Name != "" {
		return c.peer.Name
	}
	return c.peer.URL
}

// do : requête authentifiée. Un statut autre que 200 devient une erreur (statusError).
// body et headers sont facultatifs ; le corps peut être un flux.
func (c *syncClient) do(method, path string, query url.Values, body io.Reader, headers map[string]string) (*http.Response, error) {
	return c.doWith(c.http, method, path, query, body, headers)
}

// doWith : do avec le client donné (c.http, ou c.slow pour la réception d'une archive).
func (c *syncClient) doWith(hc *http.Client, method, path string, query url.Values, body io.Reader, headers map[string]string) (*http.Response, error) {
	resp, err := c.raw(hc, method, path, query, body, headers)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.statusError(resp)
	}
	return resp, nil
}

// raw : requête authentifiée, réponse rendue telle quelle (tout code de statut). Une machine qui ne
// répond pas, ou un certificat qui n'a pas l'empreinte du code, sont des erreurs.
func (c *syncClient) raw(hc *http.Client, method, path string, query url.Values, body io.Reader, headers map[string]string) (*http.Response, error) {
	u := c.peer.URL + path
	if query != nil {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.peer.Token)
	req.Header.Set("X-Wsq-Machine", c.id)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, errCertPin) {
			return nil, fmt.Errorf("%s : empreinte du certificat différente (machine réinstallée ? refaire --pair puis --sync wsq://…)", c.label())
		}
		return nil, &unreachableError{err: err}
	}
	return resp, nil
}

// errPairCode : le code d'association est refusé (expiré, déjà utilisé, ou faux) : 401 de POST /v1/pair.
var errPairCode = errors.New("code d'association expiré ou déjà utilisé : relancer --pair sur l'autre machine")

// errPairOld : l'autre machine n'a pas la route d'association : son lanceur est trop ancien.
var errPairOld = errors.New("lanceur trop ancien sur l'autre machine : --update là-bas, puis --pair")

// pairReply : réponse de POST /v1/pair : le jeton durable de cette machine et le nom d'hôte distant.
type pairReply struct {
	Token string
	Host  string
}

// pair : échange le code d'association (le secret, dans c.peer.Token) contre un jeton durable propre à
// cette machine. Le nom d'hôte local est envoyé pour que l'autre machine l'affiche.
func (c *syncClient) pair() (pairReply, error) {
	name, _ := os.Hostname()
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return pairReply{}, err
	}
	resp, err := c.raw(c.http, http.MethodPost, "/v1/pair", nil, bytes.NewReader(body),
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return pairReply{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return pairReply{}, errPairCode
	case http.StatusNotFound:
		return pairReply{}, errPairOld
	default:
		return pairReply{}, c.statusError(resp)
	}
	var v struct {
		Token string `json:"token"`
		Host  string `json:"host"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil || v.Token == "" {
		return pairReply{}, fmt.Errorf("%s : réponse d'association illisible", c.label())
	}
	return pairReply{Token: v.Token, Host: v.Host}, nil
}

// statusError : erreur d'une réponse non 200. Le corps est lu puis fermé.
func (c *syncClient) statusError(resp *http.Response) error {
	defer resp.Body.Close()
	var body struct {
		Detail string `json:"detail"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("jeton refusé : refaire --pair sur %s puis --sync wsq://…", c.label())
	case resp.StatusCode == http.StatusConflict && body.Detail == "jeu en cours":
		return errSyncBusy
	case body.Detail != "":
		return fmt.Errorf("%s : %s", c.label(), body.Detail)
	}
	return fmt.Errorf("%s : réponse HTTP %d", c.label(), resp.StatusCode)
}

// info : nom de la machine distante (champ host de GET /v1/info).
func (c *syncClient) info() (string, error) {
	resp, err := c.do(http.MethodGet, "/v1/info", nil, nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var v struct {
		Host string `json:"host"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil {
		return "", fmt.Errorf("%s : réponse illisible", c.label())
	}
	return v.Host, nil
}

// list : jeux de la machine distante, avec l'état enregistré pour cette machine.
func (c *syncClient) list() ([]remoteSave, error) {
	resp, err := c.do(http.MethodGet, "/v1/saves/list", nil, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v struct {
		Games []remoteSave `json:"games"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&v); err != nil {
		return nil, fmt.Errorf("%s : liste illisible", c.label())
	}
	return v.Games, nil
}

// fetch : reçoit les sauvegardes d'un jeu de la machine distante et les applique dans users.
// L'archive est d'abord copiée dans un fichier temporaire (sa taille est annoncée : une coupure
// est détectée avant toute modification). L'archivage de la version locale est à la charge de
// l'appelant. Retourne l'empreinte distante.
func (c *syncClient) fetch(game, users string) (string, error) {
	// L'agent prépare toute l'archive avant de répondre : en-têtes attendus plus longtemps.
	resp, err := c.doWith(c.slow, http.MethodGet, "/v1/saves/tar", url.Values{"game": {game}}, nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	fp := resp.Header.Get("X-Wsq-Fingerprint")
	profile := resp.Header.Get("X-Wsq-Profile")
	tmp, err := os.CreateTemp("", "wsq-recu-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return "", fmt.Errorf("%s : transfert interrompu : %w", c.label(), err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if err := applySaveArchive(tmp, users, profile); err != nil {
		return "", fmt.Errorf("archive reçue refusée : %w", err)
	}
	return fp, nil
}

// send : envoie les sauvegardes locales de users à la machine distante, qui remplace son périmètre.
// Retourne l'empreinte distante après remplacement et le chemin de l'archive de la version
// distante si elle a été mise de côté ("" sinon).
func (c *syncClient) send(game, users string) (string, string, error) {
	files := collectSaveFiles(users)
	if len(files) == 0 {
		return "", "", errors.New("rien à envoyer : aucune sauvegarde locale")
	}
	headers := map[string]string{}
	if profs := fileProfiles(files); len(profs) == 1 {
		headers["X-Wsq-Profile"] = profs[0]
	}
	pr, pw := io.Pipe()
	werrCh := make(chan error, 1)
	go func() {
		_, err := writeSaveArchive(pw, users, files)
		_ = pw.CloseWithError(err)
		werrCh <- err
	}()
	resp, err := c.do(http.MethodPut, "/v1/saves/tar", url.Values{"game": {game}}, pr, headers)
	// Libère l'écrivain s'il attend encore la lecture (réponse reçue avant la fin du corps).
	_ = pr.Close()
	if werr := <-werrCh; werr != nil && !errors.Is(werr, io.ErrClosedPipe) {
		return "", "", fmt.Errorf("archive locale : %w", werr)
	}
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var v struct {
		Fingerprint string `json:"fingerprint"`
		Archived    string `json:"archived"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil {
		return "", "", fmt.Errorf("%s : réponse illisible", c.label())
	}
	return v.Fingerprint, v.Archived, nil
}

// ack : confirme à la machine distante l'empreinte commune après une réception.
func (c *syncClient) ack(game, fp string) error {
	body, err := json.Marshal(syncStateRequest{Game: game, Fingerprint: fp})
	if err != nil {
		return err
	}
	resp, err := c.do(http.MethodPost, "/v1/saves/state", nil, bytes.NewReader(body),
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
