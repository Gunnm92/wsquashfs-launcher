# Agent pour wsquashfs-manager

L'agent expose à wsquashfs-manager les opérations du lanceur sur la machine de
jeu : état (`--doctor`), essai chronométré (`--trial`), maintenance (liste,
démontage, nettoyage, sauvegardes) et runners installés. Il fait partie du
lanceur (`wsquashfs-launcher --agent`) : aucun Python, aucun `openssl`.

## Associer une machine

1. Sur la machine de jeu : `wsquashfs-launcher --pair --roms /chemin/roms`
   (initialise jeton, certificat et dossiers roms/, affiche le code, puis
   attend l'association).
2. Dans wsquashfs-manager (Réglages → Machine de jeu → Agent réseau), coller le
   code `wsq://…` et cliquer sur **Associer**. Le manager vérifie que le
   certificat correspond au code : aucune comparaison manuelle. Pour la
   réserve de sauvegardes du manager, même code dans Réglages → Sauvegardes ;
   entre machines, `wsquashfs-launcher --sync wsq://…` sur l'autre machine.

`--pair` attend la première requête authentifiée reçue après l'affichage du
code (le manager ou `--sync wsq://…` sur l'autre machine), puis se ferme. Sans
agent en marche, il sert un agent temporaire, arrêté après l'association ; avec
un agent déjà lancé (systemd, `--agent`), il s'appuie sur lui. Ctrl-C annule
(code 130), 10 min sans association : code 1. La trace de la dernière requête
authentifiée est dans `last-client.json`, à côté de `agent.json`.

`--pair --reset` renouvelle le jeton (l'ancien ne fonctionne plus).

## Lancer l'agent

- Sans systemd (conteneur, SteamBox) : `wsquashfs-launcher --agent` lance
  l'agent en arrière-plan et rend la main ; `--agent stop` l'arrête,
  `--agent status` dit s'il tourne (pid, port, journal). Le journal est
  `~/.local/state/wsquashfs/agent.log`.
- Avec systemd utilisateur : `--setup --agent` installe une socket et un
  service (`ExecStart=… --agent run`). L'agent démarre à la première connexion et
  s'arrête seul après 60 s sans requête. Pour qu'il réponde sans session
  ouverte : `loginctl enable-linger`.
- `wsquashfs-launcher --agent run [log]` : premier plan, pour systemd et le
  débogage. `log` trace chaque requête (méthode, chemin, code, durée), sans
  jamais écrire le jeton.

## Sécurité

- HTTPS uniquement, TLS 1.3 ; certificat auto-signé ECDSA P-256 (10 ans), dont
  l'empreinte SHA-256 est dans le code d'association.
- Jeton de 32 octets (`Authorization: Bearer`), comparé à temps constant.
- Aucune commande fournie par le client : chaque route correspond à une action
  fixe. Les images sont des chemins relatifs à un dossier roms/ configuré ; les
  liens qui sortent de ces dossiers sont refusés. Les runners ne sont lus que
  s'ils sont installés sur la machine.
- Un seul essai à la fois (409 sinon).

## Configuration

`~/.config/wsquashfs/agent/` : `agent.json` (port, jeton, dossiers roms/,
processus de session), `cert.pem`, `key.pem` (droits 600).

## Routes

| Route | Rôle |
|---|---|
| `GET /v1/info` | version, machine, utilisateur, dossiers roms/, `os` (ID de `/etc/os-release`, `linux` par défaut) |
| `GET /v1/doctor[?refresh=1]` | rapport `--doctor --json` (mis en cache 5 min) |
| `POST /v1/trial` `{rel, duration}` | essai : rapport `--trial` et capture (base64) |
| `POST /v1/maintenance` `{action, rel?}` | voir la table ci-dessous |
| `GET /v1/runner/list?path=` | fichiers d'un runner installé |
| `GET /v1/runner/tar?path=` | runner en tar (liens conservés) |

### Synchro des sauvegardes (`--sync`)

### Maintenance (`POST /v1/maintenance`)

Table fermée (`maintenanceActions` dans `agent_http.go`), contrat commun avec
wsquashfs-manager (version de l'agent : **4**, route `GET /v1/info`).

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
aléatoire de la machine cliente, 400 sinon) : l'état de synchro est tenu par
machine, dans `savesDir/.sync/<identifiant>/<jeu>.json`. Le périmètre est celui de
`--save` / `--saves` (sous users/, sans caches ni comptes système). La liste des jeux
(`GET /v1/saves/list`) est celle de `--sync` (`syncGames`, sync.go) : overlay, copie de travail
et mise de côté du mode copy.

| Route | Rôle |
|---|---|
| `GET /v1/saves/list` | jeux à sauvegardes (empreinte, plus récente date, `size` : taille totale en octets) et état enregistré pour cette machine |
| `GET /v1/saves/tar?game=` | archive tar.gz du périmètre (en-têtes `X-Wsq-Fingerprint`, `X-Wsq-Profile`) ; 404 sans sauvegarde |
| `PUT /v1/saves/tar?game=` | remplace le périmètre par l'archive (2 Gio max, 413 sinon) ; si la version locale a changé depuis la dernière synchro avec cette machine, elle est archivée dans le dossier backups d'abord (réponse `archived`) |
| `POST /v1/saves/state` `{game, fingerprint}` | enregistre l'empreinte commune après une réception (409 si le jeu a changé entre-temps) |

Règle du conflit (les deux côtés ont changé depuis la dernière synchro) : la version la plus lourde gagne, à taille égale la plus récente ; la perdante est archivée dans le dossier backups.

Un jeu verrouillé (lancé, monté, en maintenance) répond 409 `jeu en cours`.
