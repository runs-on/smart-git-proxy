#!/bin/sh
set -e

# Create system user/group if not exists
if ! getent group gitproxy >/dev/null; then
    groupadd --system gitproxy
fi

if ! getent passwd gitproxy >/dev/null; then
    useradd --system --gid gitproxy --home-dir /var/lib/gitproxy --shell /usr/sbin/nologin gitproxy
fi

# Create mirror directory if not already created by preinstall (ephemeral storage)
mkdir -p /var/lib/gitproxy/mirrors

# Create config directory and default env file if not exists
mkdir -p /etc/smart-git-proxy
if [ ! -f /etc/smart-git-proxy/env ]; then
    touch /etc/smart-git-proxy/env
fi

# Set proper ownership (handles both ephemeral and regular storage)
chown -R gitproxy:gitproxy /var/lib/gitproxy

# Reload systemd and enable service
systemctl daemon-reload
systemctl enable smart-git-proxy
systemctl start smart-git-proxy || true
