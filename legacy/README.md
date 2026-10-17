# Legacy flashing method

These are the original scripts and notes from the first version of this
project. Back then, getting root on a Classe 300X meant unpacking an official
`.fwz` by hand, editing the ext4 rootfs, repacking it, and flashing the result
with BTicino's MyHOME Suite on Windows. The Python helpers (`main.py`,
`bticino.py`, `diagnose_mqtt.py`), the pack/unpack shell scripts, and the
assorted comparison notes in this folder belong to that workflow.

They are kept here for reference and reproducibility. They are **not** the
recommended path anymore. The current approach builds a rooted image on the fly
from your own firmware and flashes it over USB without MyHOME Suite — see the
top-level `README.md` and the `patches/` and `hashes/` folders. Nothing in this
directory ships firmware: you still bring your own `.fwz`.
