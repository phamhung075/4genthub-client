## Installing the client creates `~/.config/4genthub/.env`

### Changed
- `scripts/build.sh`: when the checkout has no `.env` and `~/.config/4genthub/.env` does not exist, the build now creates it from `.env.sample` (mode 600, directory 700) and prints that `AGENTHUB_TOKEN` must be set. An existing installed file is never touched; a checkout `.env` still replaces it as before.
- `README.md`, `docs/AGENT-SETUP.md`: the init command no longer overwrites (`[ -f … ] || cp …`), sets mode 600, and says when to run it and that `build.sh` does it on a first install.

### Tested
- `bash -n scripts/build.sh`; the new branch run against a temporary `HOME` (see the run recorded with this change).
