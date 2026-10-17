# Security

## Reporting an issue

If you find a security problem in this project's code (the bridge, the flasher,
or the builder), please report it privately first. Open a GitHub security
advisory on the repository, or contact the maintainer through the address on
the GitHub profile, rather than filing a public issue with exploit details.
Give it a reasonable window to be fixed before disclosing publicly.

This is a hobby project maintained in spare time; there is no formal SLA, but
reports are read and taken seriously.

## A built image carries your secrets

The firmware builder bakes your configuration into the image so the device is
ready on first boot. That image is therefore **personal**:

- It can contain your Home Assistant address and a long-lived access token.
- It is tailored to your installation.

Treat a built `.fwz` like a password. **Do not share it, upload it, or post it
in support threads.** If you need help, share logs and the build command, never
the baked image. If a built image leaks, rotate the Home Assistant token it
contains.

## Scope

Reports about this project's own code are in scope. Vulnerabilities in BTicino
firmware or the official cloud are not ours to fix — report those to BTicino /
Legrand.
