#!/usr/bin/env bash
# CI's deploy job, on the box: an SSH forced command (README.md, "Auto-deploy")
# that brings this checkout and both images up to main, whichever repo pushed.
# A push to each repo at once queues on the lock rather than racing. Output goes
# to the journal, not back to CI: both repos' Actions logs are public.
set -euo pipefail
cd "$(dirname "$0")"
exec 9>.auto-deploy.lock
flock 9
if systemd-cat -t new-commerce-deploy git -C .. pull --ff-only origin main &&
	systemd-cat -t new-commerce-deploy ./deploy.sh main; then
	echo "deployed main ($(git -C .. rev-parse --short HEAD)) for ${SSH_ORIGINAL_COMMAND:-a manual run}"
else
	echo "deploy failed: journalctl -t new-commerce-deploy on the box" >&2
	exit 1
fi
