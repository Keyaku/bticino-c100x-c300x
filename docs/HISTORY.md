# A note on repository history

This repository does not track firmware images. Official `.fwz` files, rootfs
images, and kernels are excluded by `.gitignore`, and the standing check in
`tools/check-repo-hygiene.sh` fails the build if one is ever added.

Earlier in the project's life, some large firmware blobs *were* committed. They
still live in the old git history, so a full `git clone` and any existing fork
continue to carry them. We chose **not** to rewrite history to strip them:
rewriting would change every commit hash, break outstanding forks and clones,
and invalidate links people have already shared. The cost of that disruption is
higher than the benefit of a smaller `.git`, especially since the files are
BTicino's own official releases rather than secrets.

What this means for you:

- The current tree is clean — it contains hashes and tools, not firmware.
- If you need an official image, obtain it from BTicino/Legrand through the
  normal channels and verify it against `hashes/known-good.sha256`. See
  `docs/bring-your-own-firmware.md`.
- If the historical blobs bother you for a mirror or a fork, you can run your
  own `git filter-repo` locally; we document the decision rather than impose it.
