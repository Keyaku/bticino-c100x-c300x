# BTicino Classe 300X — rooted firmware for Home Assistant

Run your BTicino Classe 300X video door entry unit with
[Home Assistant](https://www.home-assistant.io/) — answer, see, hear, and open
the door from HA — without the official cloud app, and ideally without
Windows-only MyHOME Suite.

This project builds a **rooted firmware image from the official firmware you
already own**, bakes in a Home Assistant bridge so the device is ready on first
boot, and flashes it over USB. The old way (hand-editing firmware and flashing
with MyHOME Suite) still lives under [`legacy/`](legacy/); this README describes
the current approach.

> **Supported today: Classe 300X.** Classe 100X is on the roadmap, not a
> finished build target yet — see the status below.

## Support this work

If it helped you, donations keep the development going:
[PayPal](https://www.paypal.com/donate/?business=XQQJHGVPHLD7W&no_recurring=0&item_name=Thank+you+for+collaborating+and+helping+the+development+of+software+improvements.&currency_code=EUR)

## How it works, in three steps

1. **Build a rooted image, on the fly, from your own firmware.** You provide an
   official `.fwz` (the project never ships firmware — see
   [bring your own firmware](docs/bring-your-own-firmware.md)). The builder
   opens it, edits the root filesystem offline, and bakes in the bridge, its
   startup integration, and your configuration. Each image is built locally and
   is personal to you.
2. **Flash it over USB, no MyHOME Suite.** A cross-platform Go flasher talks to
   the device over USB (and can over the LAN) using the device's own OpenWebNet
   protocol plus FTP. It runs read-only by default and gates every write behind
   an explicit flag and a verified backup.
3. **First boot is ready for Home Assistant.** The baked-in bridge starts by
   itself and exposes the device to HA over MQTT (with auto-discovery),
   HomeKit, a web dashboard, and RTSP video. Nothing to configure by hand on
   the device after flashing.

See the diagrams in [`docs/`](docs/) for the end-to-end flow, the flasher
protocol, the builder pipeline, the bridge architecture, and the optional
cloud-survival mode.

## Model status

| Model       | Build a rooted image | Flash over USB | Bridge in HA | Status   |
| ----------- | -------------------- | -------------- | ------------ | -------- |
| Classe 300X | yes                  | yes            | yes          | supported |
| Classe 100X | roadmap              | roadmap        | roadmap      | roadmap  |

The `.fwz` packaging is the same across both models (the archive password is
just the model name), but the 100X has not been validated end to end here yet.
For a 100X path today, see the related projects below.

## What is in this repository

| Path                                   | What it is                                              |
| -------------------------------------- | ------------------------------------------------------- |
| `flasher/`                             | Go USB/LAN flasher (Subsystem B — **in progress**; see cross-references once it lands) |
| `firmware-builder/`                    | On-the-fly rooted-image builder (Subsystem C — **in progress**) |
| [`bticino_bridge/`](bticino_bridge/README.md) | The Home Assistant bridge that gets baked into the image |
| [`patches/`](patches/)                 | Small bake-in pieces for the bridge (init, firewall, iptables, a daemon patch) |
| [`hashes/`](hashes/README.md)          | Known-good checksums of official firmware (no firmware itself) |
| [`docs/`](docs/README.md)              | Guides, diagrams, protocol and command reference        |
| [`legacy/`](legacy/)                   | The original hand-edit + MyHOME Suite method, kept for reference |

The flasher and builder are being brought over as their own subsystems; until
they land, this README points to where they will be and the bridge is already
usable on a rooted device.

## Related projects

Independent efforts worth knowing about, by jorgeavlobo:

- [`intercom-firmware-tool`](https://github.com/jorgeavlobo/intercom-firmware-tool)
  — a firmware tool with a Windows-friendly flow.
- [`bticino-classe100x-ha`](https://github.com/jorgeavlobo/bticino-classe100x-ha)
  — a lighter Classe 100X ↔ Home Assistant integration.

This project aims to coordinate with, not duplicate, that work.

## Device integration reference

- [OpenWebNet commands and discovery](docs/openwebnet-commands.md) — the known
  command/notification table and how to find new ones.
- [`mqtt_scripts/`](mqtt_scripts/README.md) — the generic MQTT bridge scripts
  (the manual, bridge-free integration path).

## Safety, legality, and license

- You bring your own firmware; this project does not host or redistribute
  BTicino images. See [`docs/bring-your-own-firmware.md`](docs/bring-your-own-firmware.md).
- Flashing can brick the unit, and cloud-survival disables vendor OTA. Read
  [`THREAT_MODEL.md`](THREAT_MODEL.md) before flashing.
- How the project was built, without proprietary code or firmware:
  [`CLEANROOM.md`](CLEANROOM.md). To report a security issue: [`SECURITY.md`](SECURITY.md).
- Licensed under the GNU GPL v2 — see [`LICENSE`](LICENSE).

## Origin

This work grew out of the community Telegram channel:
https://t.me/bTicinoClasse300x
