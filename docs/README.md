# Documentation

Guides and reference for building a rooted Classe 300X image, flashing it, and
running it with Home Assistant.

## Start here

- [Bring your own firmware](bring-your-own-firmware.md) — why you supply the
  official image, where to get it, and how to verify it.
- [Known-good hashes](../hashes/README.md) — checksums of the official firmware
  this project was tested against.

## Reference

- [OpenWebNet commands and discovery](openwebnet-commands.md) — the command
  table and how to find new frames.
- [The bridge](../bticino_bridge/README.md) — the Home Assistant bridge that is
  baked into the image.
- [App versus bridge](app-vs-bridge.md) — what the bridge replaces, what still
  needs the cloud, and the one-time provisioning flow.

## Safety and legal

- [Threat model and risks](../THREAT_MODEL.md)
- [Clean-room statement](../CLEANROOM.md)
- [Security policy](../SECURITY.md)
- [A note on repository history](HISTORY.md)
- [The legacy method](https://github.com/fquinto/bticinoClasse300x/tree/main/legacy) (upstream; this fork keeps it at the repo root, see the main README)

## Tooling status

The Go flasher (`flasher/`) and the on-the-fly firmware builder
(`firmware-builder/`) are being brought in as their own subsystems. Until they
land, this folder documents the flow and the bridge is already usable on a
rooted device.

## Diagrams

- [End-to-end flow](diagrams/01-end-to-end.svg) — from your firmware to a device
  running in Home Assistant.
- [Flasher protocol](diagrams/02-flasher-protocol.svg) — the USB link, the
  write state machine, and the safety gates.
- [Builder pipeline](diagrams/03-builder-pipeline.svg) — how a rooted image is
  built offline from your `.fwz`.
- [Bridge architecture](diagrams/04-bridge-architecture.svg) — the bridge
  between the native stack and Home Assistant, and the non-interference rule.
- [Cloud-survival mode](diagrams/05-cloud-survival.svg) — what is cut or
  redirected and what keeps working, reversibly.
- [App versus bridge](diagrams/06-app-vs-bridge.svg) — the app's two planes, what
  the bridge replaces, and the cloud leftovers.
