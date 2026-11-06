# Agent pour wsquashfs-manager

L'agent expose à wsquashfs-manager les opérations du lanceur sur la machine de
jeu : état (`--doctor`), essai chronométré (`--trial`), maintenance (liste,
démontage, nettoyage, sauvegardes) et runners installés. Il fait partie du
lanceur (`wsquashfs-launcher --agent`) : aucun Python, aucun `openssl`.

## Associer une machine

1. Sur la machine de jeu : `wsquashfs-launcher --pair --roms /chemin/roms`
   (initialise jeton, certificat et dossiers roms/, puis affiche le code).
2. Dans wsquashfs-manager (Réglages → Machine de jeu → Agent réseau), coller le
   code `wsq://…` et cliquer sur **Associer**. Le manager vérifie que le
   certificat correspond au code : aucune comparaison manuelle.

`--pair --reset` renouvelle le jeton (l'ancien ne fonctionne plus).

## Lancer l'agent

- Avec systemd utilisateur : `--setup --agent` installe une socket et un
  service. L'agent démarre à la première connexion et s'arrête seul après 60 s
  sans requête. Pour qu'il réponde sans session ouverte : `loginctl enable-linger`.
- Sans systemd (conteneur, SteamBox) : `wsquashfs-launcher --agent`.
- `wsquashfs-launcher --agent log` trace chaque requête (méthode, chemin,
  code, durée), sans jamais écrire le jeton.

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
| `GET /v1/info` | version, machine, utilisateur, dossiers roms/ |
| `GET /v1/doctor[?refresh=1]` | rapport `--doctor --json` (mis en cache 5 min) |
| `POST /v1/trial` `{rel, duration}` | essai : rapport `--trial` et capture (base64) |
| `POST /v1/maintenance` `{action, rel?}` | list, umount, clean, saves (un jeu ou tous) |
| `GET /v1/runner/list?path=` | fichiers d'un runner installé |
| `GET /v1/runner/tar?path=` | runner en tar (liens conservés) |
