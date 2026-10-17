# Bring your own firmware

This project does not host, mirror, or redistribute BTicino firmware. The
tooling builds a rooted image *from an official release that you provide*, and
flashes that. You need to obtain the original `.fwz` yourself.

## Why you supply it

The firmware is BTicino/Legrand's copyrighted property. Shipping it here would
be redistribution we have no right to do, and it would also tie the repository
to specific versions and bloat it. Keeping firmware out of the repo keeps the
project on the right side of that line: it ships knowledge and tools, you bring
the image.

## Where to get an official image

- BTicino / Legrand official channels for your country.
- The MyHOME Suite / MyHOME_Suite update packages, which contain the per-model
  `.fwz` files.
- Any image you have already pulled from your own device or your own installer
  software.

Only use images for the model you own (Classe 300X for the supported path).

## Verify before you build

Check the file against the known-good list:

```sh
cd /folder/with/your/firmware
sha256sum -c /path/to/repo/hashes/known-good.sha256
```

- A listed version prints `OK` — you have a clean, expected image.
- A version that is **not** listed is allowed, but the builder will warn you
  that the image is unrecognized and ask you to confirm before continuing. This
  is deliberate: it catches a truncated download or a wrong-model file without
  locking you to the two versions we happen to have tested.

See `../hashes/README.md` for the checksum details and how to contribute a new
hash.
