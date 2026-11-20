# Pont DualSense Bluetooth → USB

Pont uhid, intégré au lanceur (paquet `internal/dsbridge`, commande interne
`--dualsense-bridge`), qui présente chaque DualSense **Bluetooth** comme une
DualSense **USB**. `wsquashfs-launcher` le démarre pour les jeux en
`HIDRAW=1` et l'arrête avec eux.

## Le problème

La DualSense virtuelle d'un streaming à distance est toujours en Bluetooth
(bus `0005`). Les `libScePad.dll` de 2022 (plusieurs jeux concernés) l'énumèrent
sans jamais l'ouvrir : aucun rapport feature lu, aucune écriture, le jeu ne
réagit à aucune touche. Une DualSense USB est initialisée tout de suite
(constaté le 02/10/2026 sur un jeu concerné).

## Fonctionnement

| Sens | DualSense Bluetooth | DualSense USB du pont |
|------|---------------------|-----------------------|
| entrée | rapport `0x31`, 78 octets | rapport `0x01`, 64 octets : même bloc commun, sans l'octet d'étiquette Bluetooth |
| sortie | rapport `0x31`, 78 octets : séquence/étiquette, bloc commun, CRC32 | rapport `0x02`, 48 octets : vibration, barre lumineuse, gâchettes adaptatives |
| feature | `0x05` calibration, `0x09` appairage, `0x20` firmware : lus sur la manette Bluetooth | mêmes tailles ; l'adresse MAC d'appairage est remplacée par celle du pont |

Sans cette adresse MAC propre, `hid-playstation` refuse la manette USB comme
doublon de la Bluetooth (même manette branchée deux fois).

Avec `--hide`, le nœud `/dev/hidrawN` de la manette Bluetooth passe en `600`
(`sudo -n chmod` si besoin) jusqu'à l'arrêt du pont : Wine ne voit que la
manette USB, pas un doublon. Les accès déjà ouverts (Steam, streaming à distance) restent
valables. Si Moonlight se reconnecte, la nouvelle DualSense Bluetooth est
reliée à la même manette USB : le jeu ne la perd pas.

## Limites

- **Pas d'haptiques** : en USB, les jeux les jouent en audio (4 canaux) sur la
  carte son de la manette, qui n'existe pas ici. Pour un jeu dont les
  haptiques passent en rapports Bluetooth `0x32`–`0x39`, utiliser
  `HIDRAW=bt`.
- Les écritures de rapports feature (`0x08`…) sont acquittées sans être
  transmises.

## Utilisation

```bash
wsquashfs-launcher --dualsense-bridge --hide --parent $$ --ready-file /tmp/pret
```

- `--hide` : masque les DualSense Bluetooth tant que le pont tourne.
- `--parent PID` : s'arrête quand ce processus se termine.
- `--ready-file F` : crée `F` une fois les manettes USB en place.

`/dev/uhid` doit être accessible en écriture, et les `/dev/hidrawN` des DualSense
en lecture et écriture.

## Implémentation

Le paquet `internal/dsbridge` reprend octet pour octet l'ancien script Python
(rapports `report.go`, périphériques uhid `bridge.go`, boucle `dsbridge.go`).
La boucle est une seule goroutine sur `unix.Poll` (délai de 1 s : nouvelles
manettes, surveillance du parent) ; une goroutine de signaux (SIGTERM, SIGINT,
SIGHUP) vit le temps de `Serve`. À l'arrêt, les périphériques uhid sont
détruits et les droits des `hidraw` restaurés.
