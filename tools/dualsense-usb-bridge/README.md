# dualsense-usb-bridge — DualSense Bluetooth vue comme une DualSense USB

Pont uhid (Python 3, sans dépendance) qui présente chaque DualSense
**Bluetooth** comme une DualSense **USB**. `wsquashfs-launcher` le démarre pour
les jeux en `HIDRAW=1` et l'arrête avec eux.

## Le problème

La DualSense virtuelle de Sunshine est toujours en Bluetooth (bus `0005`). Les
`libScePad.dll` de 2022 (F1 22 ; même version dans The Devil in Me et
The King of Fighters XV) l'énumèrent sans jamais l'ouvrir : aucun rapport
feature lu, aucune écriture, le jeu ne réagit à aucune touche. Une DualSense
USB est initialisée tout de suite (constaté le 02/10/2026 sur F1 22).

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
manette USB, pas un doublon. Les accès déjà ouverts (Steam, Sunshine) restent
valables. Si Moonlight se reconnecte, la nouvelle DualSense Bluetooth est
reliée à la même manette USB : le jeu ne la perd pas.

## Limites

- **Pas d'haptiques** : en USB, les jeux les jouent en audio (4 canaux) sur la
  carte son de la manette, qui n'existe pas ici. Pour un jeu dont les
  haptiques passent en rapports Bluetooth `0x32`–`0x39` (Until Dawn), utiliser
  `HIDRAW=bt`.
- Les écritures de rapports feature (`0x08`…) sont acquittées sans être
  transmises.

## Utilisation

```bash
dualsense-usb-bridge --hide --parent $$ --ready-file /tmp/pret
```

- `--hide` : masque les DualSense Bluetooth tant que le pont tourne.
- `--parent PID` : s'arrête quand ce processus se termine.
- `--ready-file F` : crée `F` une fois les manettes USB en place.

`/dev/uhid` doit être accessible en écriture, et les `/dev/hidrawN` des DualSense
en lecture et écriture.
