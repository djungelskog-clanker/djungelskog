#!/bin/bash
# one-shot: remove cc-connect after the switch to the go bot (2026-10-29)
exec >>"$HOME/workspace/slackbot/data/remove-cc-connect.log" 2>&1
set -x
export PATH="$HOME/.npm-global/bin:/usr/local/bin:/usr/bin:/bin"
systemctl --user disable --now cc-connect.service
rm -f "$HOME/.config/systemd/user/cc-connect.service"
rm -rf "$HOME/.config/systemd/user/cc-connect.service.d"
systemctl --user daemon-reload
systemctl --user reset-failed cc-connect.service
npm uninstall -g cc-connect
rm -rf "$HOME/.cc-connect" "$HOME/workspace/cc-connect-src" \
  "$HOME/cc-connect.bin.orig-v1.5.0" "$HOME/cc-connect.bin.patched-v1.5.1" /tmp/probe
echo "done $(date)"
