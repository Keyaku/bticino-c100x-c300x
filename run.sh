#!/usr/bin/env bash

set -e

if ! command -v python3 &>/dev/null; then
	echo "python3 is not installed and is required. Install it with the package manager of your choice."
	exit 1
fi

typeset -r THIS="$(realpath "$0")"
typeset -r THIS_DIR="${THIS%/*}"
typeset -r THIS_NAME="${THIS##*/}"

cd "${THIS_DIR}"

# Set up local Python virtual environment if user hasn't one active
if [ -z "$VIRTUAL_ENV" ]; then
	[ -f .venv/pyvenv.cfg ] || python -m venv --system-site-packages .venv
	source .venv/bin/activate
fi

function file_older_than_days {
	(( 0 < $# && $# <= 2 )) || return 1
	[ -f "$1" ] || return 0 # assume file is older since it doesn't exist
	local filename=$1
	local days=$(( $2 * 24 * 3600 ))
	local -i date_diff=$(( $(date +%s) - $(date -r $filename +%s) ))
	(( $days < date_diff ))
}

mkdir -p tmp

# Check if requirements weren't updated in a week
if file_older_than_days tmp/pip-required.txt 7; then
	pip freeze -r requirements.txt 2>tmp/pip-required.txt >/dev/null || true
fi
# Install requirements if necessary. `pip freeze -r` writes the missing ones to
# stderr, so a non-empty file means something still needs installing.
if [ ! -s tmp/pip-required.txt ] || grep -qv '^$' tmp/pip-required.txt; then
	pip install --upgrade pip
	pip install -r requirements.txt
	pip freeze -r requirements.txt 2>tmp/pip-required.txt >/dev/null || true
fi

# main.py loop-mounts the image and edits /etc/shadow inside it, so it needs
# root — but the venv above is the invoking user's, so hand sudo its interpreter.
if [ "$(id -u)" -eq 0 ]; then
	python main.py
else
	sudo "$(command -v python)" main.py
fi
