// Package savesync : synchronisation manuelle des sauvegardes de partie entre
// machines du réseau local (--sync). Cœur local sans réseau (périmètre, empreintes,
// archives tar/gzip, état par machine), client HTTPS vers l'agent d'une autre
// machine (certificat épinglé), dossiers déclarés et jeux Switch d'Eden, envoi
// automatique à la sortie d'un jeu, et la commande --sync elle-même (Run). Le
// paquet n'arrête jamais le processus : Run renvoie le code de sortie, les autres
// fonctions renvoient des erreurs. Ce qu'il tient du lanceur (dossiers, verrou d'un
// jeu, archivage, agent) lui est passé par Env.
package savesync

import (
	"os"
	"time"
)

// DefaultMaxBody : taille maximale par défaut d'une archive reçue (Env.MaxBody).
const DefaultMaxBody int64 = 2 << 30

// Env : ce que la synchro reçoit du lanceur. Les champs fonction sont facultatifs
// quand l'usage ne les touche pas (client seul, tests) ; appelés sans être définis,
// ils se comportent comme « rien à faire » (voir chaque champ).
type Env struct {
	CacheDir string // cache du lanceur (conf.cacheDir) : copies de travail, verrous, archives temporaires
	SavesDir string // sauvegardes du lanceur (conf.savesDir) : une couche par jeu, état de synchro (.sync)

	LockIdleGame    func(name string) (*os.File, bool)                // verrou d'un jeu arrêté (faux : il tourne) ; absent : jamais verrouillé
	GameUsesSaveDir func(name string) bool                            // le jeu déclare SAVEDIR/SAVEFILES (non synchronisé) ; absent : faux
	ArchiveUsers    func(name, parent, member string) (string, error) // archive <parent>/<member> dans le dossier des backups ; absent : erreur
	CopyUsersStash  func(name string) string                          // mise de côté de drive_c/users en mode copy ; absent : aucune
	BackupDir       func() string                                     // dossier des backups (affiché)
	AgentRunning    func() bool                                       // l'agent de cette machine tourne-t-il ?
	CertFingerprint func() (string, error)                            // empreinte du certificat de l'agent de cette machine
	AgentRoms       func() []string                                   // dossiers roms/ de l'agent (noms des jeux Switch)
	EdenRunning     func() bool                                       // Eden tourne-t-il ? Absent : détection dans /proc

	MaxBody            int64         // taille maximale d'une archive reçue ; 0 : DefaultMaxBody
	HeaderTimeout      time.Duration // attente des en-têtes d'une réponse ; 0 : 30 s
	FetchHeaderTimeout time.Duration // idem pour la réception d'une archive ; 0 : 10 min
	AutoDialTimeout    time.Duration // connexion au manager à la sortie d'un jeu ; 0 : 2 s
}

// Syncer : la synchro des sauvegardes d'une machine, avec son environnement.
type Syncer struct {
	env Env
}

// New : synchro pour cet environnement.
func New(env Env) *Syncer {
	return &Syncer{env: env}
}

// maxBody : taille maximale d'une archive reçue.
func (s *Syncer) maxBody() int64 {
	if s.env.MaxBody > 0 {
		return s.env.MaxBody
	}
	return DefaultMaxBody
}

func orDuration(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}
