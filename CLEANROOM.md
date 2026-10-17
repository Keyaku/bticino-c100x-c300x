# Clean-room statement

This project is the result of studying a device the author owns, on the
author's own network, using publicly available tools. It is published so other
owners can use their own hardware with Home Assistant. It is not affiliated
with, endorsed by, or supported by BTicino or Legrand.

## How the knowledge was obtained

- Observing a Classe 300X the author owns: its configuration files, its local
  network traffic, and its behavior on the author's LAN.
- Static analysis of software the author already had a license to run
  (the companion app, MyHOME Suite), to understand the protocols the device
  speaks on the local network.
- Reading the device's own open protocol (OpenWebNet) and public documentation.

## What this repository does and does not contain

- **Does not contain** any BTicino firmware image, rootfs, or kernel. You bring
  your own official image (see `docs/bring-your-own-firmware.md`).
- **Does not contain** decompiled or disassembled proprietary source code.
  Findings are expressed as our own descriptions, scripts, and patches.
- **Does contain** our own code (the bridge, the flasher, the builder), small
  integration patches we wrote, checksums, and documentation.

## Intended use

Use this only on devices you own or are authorized to modify. Modifying a
device's firmware can void warranties and can brick the unit. See
`THREAT_MODEL.md` for what the tooling touches and the risks involved.
