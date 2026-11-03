# wsquashfs-agent

Agent de machine de jeu pour [wsquashfs-manager](../../../wsquashfs-manager).
Il expose à distance quelques opérations du lanceur : état (`--doctor`),
lancement d'essai, runners installés (copie pour réparer les liens du runner).

## Installation

```bash
bash install.sh --agent          # ou répondre oui à la question
```

L'installeur demande le dossier `roms/` de la machine, génère un jeton et un
certificat HTTPS auto-signé (valable 10 ans), installe deux unités systemd
utilisateur (`wsquashfs-agent.socket` sur le port 8790, à choisir à l'installation, et le service), puis
affiche l'adresse, le jeton et l'empreinte du certificat à reporter dans
wsquashfs-manager (Réglages → Machine de jeu → Agent réseau).

## Activation à la demande

Au repos, **aucun processus** : systemd ne tient que la socket. La première
connexion du gestionnaire lance le service, qui s'arrête seul après 60 s sans
requête. Sans session ouverte, l'agent ne répond pas ; pour qu'il réponde
en permanence : `loginctl enable-linger $USER` (proposé par l'installeur).

Sans systemd utilisateur (conteneur comme SteamBox) :

```bash
wsquashfs-agent serve --standalone     # reste lancé, sur le port configuré
```

## Commandes

| Commande | Rôle |
|---|---|
| `init [--roms DIR]...` | jeton, certificat, configuration |
| `serve [--standalone]` | serveur HTTPS |
| `pair` | adresses, jeton, empreinte du certificat |
| `reset-token` | nouveau jeton (l'ancien ne fonctionne plus) |
| `trial <image> [durée]` | essai en local, sortie brute |

## Ce qu'il fait, et ce qu'il ne fait pas

Routes HTTPS, avec `Authorization: Bearer <jeton>` :

- `GET /v1/info`, `GET /v1/doctor`
- `POST /v1/trial` `{rel, duration}` : `rel` est le chemin relatif à un
  dossier `roms/` configuré (refus de `..`, des chemins absolus et des liens
  qui sortent des dossiers)
- `GET /v1/runner/list?path=`, `GET /v1/runner/tar?path=` : seulement les
  runners que `--doctor` déclare installés

Il n'exécute aucune commande fournie par le client. Un seul essai à la fois
(409 sinon). Configuration : `~/.config/wsquashfs-agent/` (droits 600 pour le
jeton et la clé).

## Désinstallation

`bash install.sh --uninstall` retire l'agent et ses unités. Le jeton et le
certificat restent dans `~/.config/wsquashfs-agent/`.

## Tests

`python3 tests/test-agent.py` (authentification, chemins hostiles, runners,
activation par socket, arrêt après inactivité).
