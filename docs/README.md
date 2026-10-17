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

## Safety and legal

- [Threat model and risks](../THREAT_MODEL.md)
- [Clean-room statement](../CLEANROOM.md)
- [Security policy](../SECURITY.md)
- [A note on repository history](HISTORY.md)
- [The legacy method](../legacy/README.md)

## Tooling status

The Go flasher (`flasher/`) and the on-the-fly firmware builder
(`firmware-builder/`) are being brought in as their own subsystems. Until they
land, this folder documents the flow and the bridge is already usable on a
rooted device.

## Diagrams

Diagrams of the end-to-end flow, the flasher protocol, the builder pipeline, the
bridge architecture, and cloud-survival mode live in `diagrams/`.
