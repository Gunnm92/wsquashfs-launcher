# Agent pour wsquashfs-manager

L'agent expose à wsquashfs-manager les opérations du lanceur sur la machine de
jeu : état (`--doctor`), essai chronométré (`--trial`), maintenance (liste,
démontage, nettoyage, sauvegardes) et runners installés. Il fait partie du
lanceur (`wsquashfs-launcher --agent`) : aucun Python, aucun `openssl`.

## Associer une machine

Le code d'association `wsq://SECRET@HÔTE:PORT#EMPREINTE` est **à usage unique** : il
ne sert qu'à une association, et seulement pendant la durée d'attente de `--pair`
(10 min). Chaque machine associée reçoit son **propre jeton durable** ; les machines
ont toutes les mêmes droits (sauvegardes, images, essais, maintenance).

1. Sur la machine de jeu : `wsquashfs-launcher --pair roms /chemin/roms`
   (initialise certificat et dossiers roms/, affiche le code, puis attend
   l'association).
2. Dans wsquashfs-manager (Réglages → Machine de jeu → Agent réseau), coller le
   code `wsq://…` et cliquer sur **Associer**. Le manager vérifie que le
   certificat correspond au code : aucune comparaison manuelle. Pour la
   réserve de sauvegardes du manager, même code dans Réglages → Sauvegardes ;
   entre machines, `wsquashfs-launcher --sync wsq://…` sur l'autre machine.

L'association est un échange : la machine qui a le code appelle `POST /v1/pair`
(secret du code en `Authorization`, identifiant de sa machine en `X-Wsq-Machine`),
et reçoit son jeton durable en réponse. Le code est retiré à cet usage.

`--pair` attend l'association (la route `POST /v1/pair` écrit `last-pair.json`),
puis se ferme : « Associé : <nom> (<adresse>) ». Une requête authentifiée d'une
machine déjà associée ne termine pas `--pair`. Sans agent en marche, il sert un
agent temporaire, arrêté après l'association ; avec un agent déjà lancé
(`--agent`), il s'appuie sur lui. Ctrl-C annule (code 130), 10 min sans
association : code 1. Dans tous les cas de fin, le code en attente (`pair.json`)
est retiré. Un code expiré, déjà utilisé ou faux : 401 (« relancer --pair »).

- `--pair reset` : retire **toutes** les machines associées (révocation générale),
  le dit, puis attend une nouvelle association comme `--pair`.
- `--pair forget <machine>` : retire la machine dont le nom (sans tenir compte de
  la casse) ou l'identifiant est donné, affiche le résultat, et se termine sans
  association. Effet immédiat : son jeton est refusé à la requête suivante, agent
  en marche ou non. Machine inconnue : erreur avec la liste des noms.
- `wsquashfs-launcher --agent status` liste les machines associées (nom, date).

## Lancer l'agent

- `wsquashfs-launcher --agent` lance l'agent en arrière-plan et rend la main ;
  `--agent stop` l'arrête (le port est fermé), `--agent status` dit s'il tourne
  (pid, port, journal). L'arrêt (`--agent stop`, SIGTERM, Ctrl-C) est propre :
  le port se ferme tout de suite, les requêtes en cours ont 3 s pour finir,
  puis elles sont coupées (un envoi de sauvegardes coupé ne remplace rien). Le journal est `~/.local/state/wsquashfs/agent.log`.
- **Rien ne le démarre tout seul** : pas de service ni de socket systemd, aucun
  port ouvert en dehors de `--pair` (le temps de l'association) et d'un
  `--agent` lancé à la main. `--pair` et `--agent` initialisent l'agent
  (certificat ; les jetons n'existent que par machine associée) s'il ne l'est
  pas. Un agent sans machine associée tourne et refuse tout sauf `POST /v1/pair`.
  Les unités systemd des anciennes versions
  (`wsquashfs-agent.socket`, qui gardait le port ouvert à chaque session) sont
  retirées par `--agent`, `--agent stop`, `--setup` et `--update` ;
  `--agent status` les signale tant qu'elles sont là.
- `wsquashfs-launcher --agent run [log]` : premier plan, pour le
  débogage. `log` trace chaque requête (méthode, chemin, code, durée), sans
  jamais écrire le jeton.

## Sécurité

- HTTPS uniquement, TLS 1.3 ; certificat auto-signé ECDSA P-256 (10 ans), dont
  l'empreinte SHA-256 est dans le code d'association.
- Un jeton durable de 32 octets par machine associée (`Authorization: Bearer`).
  L'agent ne garde que son SHA-256 (`token_sha256`) : jamais le jeton en clair.
  Le jeton ne vaut que pour sa machine : un en-tête `X-Wsq-Machine` différent de
  l'identifiant de la machine du jeton est refusé (401). Les jetons sont comparés à
  temps constant, agent.json relu à chaque requête (un `forget` ou `reset` prend
  effet tout de suite).
- Le code d'association n'est gardé que par son empreinte (`pair.json`, droits 600) :
  usage unique, expiration, un mauvais secret ne le consomme pas.
- Aucune commande fournie par le client : chaque route correspond à une action
  fixe. Les images sont des chemins relatifs à un dossier roms/ configuré ; les
  liens qui sortent de ces dossiers sont refusés. Les runners ne sont lus que
  s'ils sont installés sur la machine.
- Un seul essai à la fois (409 sinon).

## Configuration

`~/.config/wsquashfs/agent/` :

- `agent.json` : port, dossiers roms/, processus de session, `games_dir` ; `machines`,
  une entrée par machine associée : `{"id", "name", "token_sha256", "paired"}`. Pas de
  champ `token` : un ancien fichier qui en a un se lit sans erreur, le champ disparaît
  à la prochaine écriture (et son jeton ne donne plus accès : les associations sont
  à refaire une fois).
- `pair.json` (droits 600) : code d'association en attente, `{"secret_sha256", "expires"}`.
  Écrit par `--pair`, retiré à l'usage, à l'expiration constatée et à la fin de `--pair`.
- `last-pair.json` (droits 600) : dernière association réussie, `{"at", "addr", "id", "name"}`.
- `cert.pem`, `key.pem` (droits 600).

## Routes

| Route | Rôle |
|---|---|
| `POST /v1/pair` | association (hors du contrôle de jeton), voir ci-dessous |
| `GET /v1/info` | version, machine, utilisateur, dossiers roms/, `os` (ID de `/etc/os-release`, `linux` par défaut), `games_dir` (dossier d'installation effectif), `games_dir_set` (choix manuel en vigueur), `manager` (empreinte du manager déclaré, `""` sinon) et `auto` (synchro automatique avec le manager, `--sync auto on` : envoi à la sortie d'un jeu, réception de sa version la plus récente) |
| `GET /v1/doctor[?refresh=1]` | rapport `--doctor json` (mis en cache 5 min) |
| `POST /v1/trial` `{rel, duration}` | essai : rapport `--trial` et capture (base64) |
| `POST /v1/maintenance` `{action, rel?}` | voir la table ci-dessous |
| `GET /v1/runner/list?path=` | fichiers d'un runner installé |
| `GET /v1/runner/tar?path=` | runner en tar (liens conservés) |

Toutes les routes sauf `POST /v1/pair` exigent un jeton de machine associée (401
« jeton refusé » sinon).

### Association (`POST /v1/pair`)

- En-têtes : `Authorization: Bearer <secret du code>` ; `X-Wsq-Machine: <identifiant>`
  obligatoire et valide (400 sinon).
- Corps JSON, 4096 octets au plus : `{"name": "<nom de la machine>"}`. Nom nettoyé
  (espaces de bord et caractères de contrôle retirés, 64 caractères au plus) ; vide :
  l'adresse du client. 413 au-delà de 4096 octets.
- Sous un verrou : `pair.json` absent, illisible ou expiré (il est alors retiré) ;
  secret différent (comparé à temps constant) : **401**
  `{"detail": "code d'association expiré ou déjà utilisé : relancer --pair"}`. Un
  mauvais secret ne consomme pas le code.
- Succès : `pair.json` est retiré d'abord (usage unique ; s'il ne peut l'être, 500 et
  rien n'est délivré) ; un jeton durable est créé ; l'entrée de même identifiant de
  `agent.json` est remplacée, ou ajoutée ; `last-pair.json` est écrit ; réponse **200**
  `{"token": "<jeton durable>", "host": "<nom d'hôte de l'agent>", "version": "10"}`.
  C'est la seule fois où le jeton sort de l'agent.
- 404 : la machine qui s'associe a un lanceur trop ancien (`--update` là-bas).
### Installation des images (`/v1/games/*`)

wsquashfs-manager installe une image `.wsquashfs` sur la machine de jeu et la désinstalle, par
ces routes (`games_http.go`). Toutes exigent le jeton ; aucune ne sort du dossier d'installation.

**Règle HOME** : le dossier d'installation est toujours un sous-dossier strict de HOME, une fois
les liens symboliques résolus (`filepath.EvalSymlinks` sur le dossier et sur HOME, puis
comparaison de préfixe avec séparateur). Hors de HOME (`/usr/local`, carte SD, lien qui sort) :
400. L'agent n'écrit que sous HOME.

**Dossier d'installation** : `games_dir` dans `agent.json` (choix manuel, défini par
`POST /v1/games/dir`) ; vide, il est détecté. Détection : parmi les dossiers sous HOME (4 niveaux
au plus) et ceux de `roms/` situés sous HOME (le dossier roms et 3 niveaux en dessous), sans suivre
les liens et en ignorant les dossiers cachés, celui qui contient directement le plus de fichiers
`.wsquashfs` ; à égalité, celui qui finit par `Games/non-steam`, sinon le premier par ordre
alphabétique ; sans image, `HOME/Games/non-steam`. Le résultat est gardé 10 minutes. Le dossier est créé
au premier envoi.

| Route | Rôle |
|---|---|
| `GET /v1/games/list` | `{dir, set, free, games: [{name, size, mtime}], parts: [{name, size}]}` : images installées et envois en cours (`.wsquashfs.part`) ; `free` en octets (dossier absent : espace du premier parent existant, listes vides) |
| `POST /v1/games/dir` `{dir}` | choisit le dossier (absolu, existant, sous HOME : 400 sinon) ; `{"dir": ""}` revient à la détection. Réponse `{dir, set}` |
| `GET /v1/games/part?name=` | `{part_size, installed, size}` : taille de l'envoi en cours (0 sans envoi), image installée ou non et sa taille |
| `PUT /v1/games/file?name=&offset=` | en-tête `X-Wsq-Total-Size` (taille totale de l'image). Ajoute le corps au `<nom>.part`. Réponse `{part_size}`. 409 `{detail: "décalage", part_size}` si `offset` n'est pas la taille reçue (reprendre là) ; 409 `déjà installé` ; 409 `envoi en cours` (un envoi à la fois par nom) ; 507 `{detail: "espace insuffisant", free}` si l'espace ne couvre pas le reste plus 1 Gio ; 413 si le corps dépasse le total |
| `POST /v1/games/commit` `{name, size, sha256}` | valide le `.part` : 404 sans envoi, 409 `{detail: "taille", part_size}` si la taille diffère, 422 `empreinte incorrecte, envoi à refaire` (le `.part` est supprimé), 409 `déjà installé`, sinon renommage en image installée. Réponse `{name, size}` |
| `DELETE /v1/games/file?name=` | supprime l'image installée et son `.part` ; 409 `jeu monté ou en cours` si le jeu tourne, est monté ou verrouillé. Réponse `{removed}` |

`name` est un nom de fichier seul (`Jeu.wsquashfs`) dont la tige passe le contrôle des noms de
jeu : sinon 400. Une coupure pendant un envoi laisse le `.part` avec ce qui a été reçu : on reprend
avec `GET /v1/games/part`, puis `PUT` à partir de `part_size`.

### Synchro des sauvegardes (`--sync`)

### Maintenance (`POST /v1/maintenance`)

Table fermée (`maintenanceActions` dans `agent_http.go`), contrat commun avec
wsquashfs-manager (version de l'agent : **10**, route `GET /v1/info`).

| `action` | Lanceur | `rel` (image du jeu) |
|---|---|---|
| `list` | `--list` | interdit |
| `umount` | `--umount` | interdit |
| `clean` | `--clean` | interdit |
| `save` | `--save <image>` | **obligatoire** (sinon 400 « --save attend un jeu ») |
| `saves` | `--saves` (tous les jeux) | interdit |
| `sync` | `--sync [image]` | facultatif |
| `sync-status` | `--sync status` | interdit |

Un `rel` interdit renvoie 400 « <action> ne prend pas de jeu » ; une action absente de la
table renvoie 400. `rel` est résolu comme une image des dossiers roms/.

Réponse : `{"action": "<action>", "code": <code de sortie du lanceur>, "output": "<sortie>"}`.
`code` vaut `-1` si le lanceur n'a pas pu démarrer ou a été interrompu par le délai ; le motif
est alors ajouté à `output`. Le manager tient un code différent de 0 pour un échec.

Ces routes servent la synchro manuelle des sauvegardes entre machines (commande
`--sync` dans `sync_cmd.go` ; cœur dans `sync.go`, client dans `sync_http.go`). Elles exigent l'en-tête `X-Wsq-Machine` (identifiant
aléatoire de la machine cliente, 400 sinon, et celui de la machine du jeton, 401 sinon) : l'état de synchro est tenu par
machine, dans `savesDir/.sync/<identifiant>/<jeu>.json`. Le périmètre est celui de
`--save` / `--saves` (sous users/, sans caches ni comptes système). La liste des jeux
(`GET /v1/saves/list`) est celle de `--sync` (`syncNames`, sync_extra.go) : jeux (overlay, copie de
travail et mise de côté du mode copy), plus les **dossiers déclarés** (`dossier:<nom>`) et les **jeux
Switch d'Eden** (`switch:<TitleID>`). Ces deux-là sont des dossiers simples, copiés tels quels (pas de
profil Wine, pas d'en-tête `X-Wsq-Profile`) ; leur `label` dans la liste est le nom lisible (nom du
dossier, « Switch · <nom du jeu> » : cache de la liste d'Eden, sinon `gamelist.xml` puis noms de fichiers
`Nom [TitleID]…` des dossiers de ROM d'Eden et de `switch/`, `switch2/` des roms de l'agent, sinon le TitleID). Une machine
où l'entrée n'existe pas (dossier non déclaré, Eden absent) répond 404 sur `tar` et `state`, et n'est
jamais modifiée. Un jeu Switch est refusé (409) tant qu'Eden tourne.

| Route | Rôle |
|---|---|
| `GET /v1/saves/list` | jeux à sauvegardes (`fingerprint`, `content` : empreinte de contenu, indépendante des dates (sha256 des chemins et des hash de fichiers), `""` si un fichier est illisible ; `newest` : plus récente date, `size` : taille totale en octets) et état enregistré pour cette machine |
| `GET /v1/saves/tar?game=` | archive tar.gz du périmètre (en-têtes `X-Wsq-Fingerprint`, `X-Wsq-Profile`) ; 404 sans sauvegarde |
| `PUT /v1/saves/tar?game=` | remplace le périmètre par l'archive (2 Gio max, 413 sinon) ; une archive sans fichier est refusée (« archive vide »), et une archive qui dépasse 4 Gio décompressés aussi : dans les deux cas rien n'est modifié (la même garde vaut à la réception côté client) ; si la version locale a changé depuis la dernière synchro avec cette machine, elle est archivée dans le dossier backups d'abord (réponse `archived`). Dossier déclaré ailleurs mais pas ici (`dossier:<nom>`) : déclaré à la réception avec le chemin de l'en-tête `X-Wsq-Dir` (à partir de HOME, encodé en pourcentage ; 400 s'il sort de HOME, 404 sans en-tête) ; la liste donne ce chemin dans `dir` |
| `POST /v1/saves/state` `{game, fingerprint}` | enregistre l'empreinte commune après une réception (409 si le jeu a changé entre-temps) |
| `GET /v1/saves/dirs` | dossiers déclarés `{dirs:[{name,path}], eden}`, chemins à partir de HOME (`eden` : dossier d'Eden détecté, `""` sinon) |
| `PUT /v1/saves/dirs` `{name, path}` | déclare (ou remplace, même nom) un dossier à synchroniser : chemin à partir de HOME (`.local/share/x`, aussi `~/…` ou absolu sous HOME ; HOME exclu), qui peut ne pas exister encore ; nom : lettres, chiffres, espace, `_` et `-` (48 au plus) ; 400 sinon. Répond la liste |
| `DELETE /v1/saves/dirs?name=` | retire la déclaration, les fichiers restent (404 nom inconnu). Répond la liste |
| `PUT /v1/saves/manager` `{name, url, token, fingerprint}` | déclare le manager (wsquashfs-manager) comme pair `manager` de cette machine : `url` en `https://hôte:port` (port obligatoire, sans chemin), `token` (lettres, chiffres, `_`, `-`), `fingerprint` (64 caractères hexadécimaux, certificat épinglé). `ui` facultatif : adresse http(s) de son interface web, ouverte par « Ouvrir le manager » de `--tray`. Remplace le manager déclaré précédemment, les autres machines restent. Réponse `{manager, auto}` ; 400 avec `detail` sinon |
| `DELETE /v1/saves/tar?game=` | supprime les sauvegardes du jeu sur cette machine : le périmètre est archivé d'abord dans le dossier backups (réponse `archived`, en 500 sans rien supprimer si l'archivage échoue) ; seuls les fichiers du périmètre partent, puis les dossiers devenus vides (caches et fichiers hors périmètre restent) ; l'état de synchro de la machine appelante pour ce jeu est effacé (réponse `removed`, nombre de fichiers). Sans sauvegarde : 200 `removed: 0`, rien d'archivé |

Empreinte de contenu : l'envoi au manager porte aussi l'en-tête `X-Wsq-Content` (même valeur que `content`). Deux copies de même contenu mais de dates différentes ont la même empreinte de contenu : la synchro les tient pour à jour (pas de faux conflit). Absente (ancien agent) ou vide, seule `fingerprint` compte. Les hash de fichiers sont gardés en mémoire (chemin, taille, date).

Règle du conflit (les deux côtés ont changé depuis la dernière synchro) : la version la plus lourde gagne ; à moins de 10 % d'écart de taille, la plus récente ; la perdante est archivée dans le dossier backups.

Poussée du manager (synchro automatique active sur la machine) : le manager envoie aussi sa version la plus récente aux machines en retard, par `PUT /v1/saves/tar` puis `POST /v1/saves/state`, sous son identifiant (`X-Wsq-Machine`). Elle n'est envoyée que si la machine n'a pas de partie non envoyée ; sinon le conflit se règle dans le manager. L'agent doit tourner (`--agent`) pour recevoir. Un jeu en cours répond 409 et la poussée est retentée. Une fois reçue, la version n'est pas renvoyée au manager à la sortie du jeu.

Un jeu verrouillé (lancé, monté, en maintenance) répond 409 `jeu en cours`.
