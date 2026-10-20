# Threat model and risks

This document describes what the tooling touches, what it deliberately leaves
alone, and the ways it can go wrong. Read it before flashing anything.

## What the tooling does

- **Firmware builder:** opens an official `.fwz` you provide, edits the root
  filesystem offline (no device involved), injects the bridge, its init/startup
  integration, and your configuration, then repacks a new `.fwz`. It never
  contacts your device and never phones home.
- **Flasher:** talks to the device over USB (RNDIS) or the LAN using the
  device's own OpenWebNet protocol plus FTP, to read model/version/status and
  to write a firmware image. The write path is gated (see below).
- **Bridge (on the device):** connects the device to Home Assistant over MQTT,
  HomeKit, a web UI, and RTSP. It is designed not to fight the native BTicino
  processes for ports or hardware.

## What it deliberately does not touch

- It does not change the device's cloud account or SIP registration.
- The bridge does not open competing sockets to the native command/video ports;
  it monitors passively and shells commands to localhost. This
  non-interference is a hard design constraint, not a preference.

## Flashing can brick the device

Writing firmware is inherently risky. A wrong image, a power loss mid-write, or
a model mismatch can leave the unit unbootable. Mitigations the flasher applies:

- A read-only preflight (model, hardware, firmware, status) before any write.
- Dry-run by default; writing requires an explicit opt-in flag.
- A device-verified backup is taken and checked before a write is allowed.
- Recovery/kernel partitions are not invalidated as part of a normal update.

Even so: have a serial console available, understand that recovery may require
one, and do not flash a device you cannot afford to lose.

## Cloud survival disables OTA

The "cloud survival" option neutralizes the device's over-the-air update and
cloud-dependent paths so a vendor update cannot silently undo your changes.
This is a reversible, local change, but while it is in place:

- The device will not receive vendor firmware updates.
- Cloud-only features of the official app may stop working.

It is a trade-off you choose, not a default we impose.
