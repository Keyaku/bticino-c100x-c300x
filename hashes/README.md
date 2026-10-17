# Firmware hashes

This folder holds checksums, not firmware. The project never distributes
BTicino images; you supply your own and the tools verify it before touching
your device.

## `known-good.sha256`

SHA-256 of the official Classe 300X releases this project has been tested
against (1.7.17 and 1.7.19). The Classe 100X 1.5.1 line is present but
commented out — it is a roadmap target, not a supported build yet.

Verify an image you already have:

```sh
cd /folder/with/your/firmware
sha256sum -c /path/to/repo/hashes/known-good.sha256
```

Each matching file prints `OK`. A file not present on disk is simply skipped.

## What happens with a version that is not listed

A newer or regional release that is not in this file is not rejected outright.
The firmware builder treats an unknown hash as a **warning**: it tells you the
image is unrecognized and asks you to confirm before it proceeds. The list is a
safety net against a corrupted or wrong-model download, not a whitelist that
freezes you to two versions. If you confirm a new official image, consider
opening a pull request adding its hash here so others benefit.
