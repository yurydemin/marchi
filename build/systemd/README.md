# systemd install

Installs Marchi as a native systemd service on Linux. This is one of two
supported install paths — see `build/docker/` for the container path.

```bash
# 1. Build the binary (or use a release tarball — see the project README)
go build -o marchi ./cmd/marchi
sudo install -o root -g root -m 0755 marchi /usr/local/bin/marchi

# 2. Dedicated system user — the service never runs as root
sudo useradd --system --home-dir /var/lib/marchi --shell /usr/sbin/nologin marchi
sudo mkdir -p /var/lib/marchi /etc/marchi
sudo chown marchi:marchi /var/lib/marchi

# 3. Config
sudo install -o root -g marchi -m 0640 config.yaml.example /etc/marchi/config.yaml
# edit /etc/marchi/config.yaml if you need anything beyond the defaults

# 4. Optional: unattended unlock (see marchi.env.example's own comments
#    for the tradeoff before setting this)
sudo install -o root -g marchi -m 0600 marchi.env.example /etc/marchi/marchi.env
# edit /etc/marchi/marchi.env and set MARCHI_MASTER_KEY

# 5. Install and start the unit
sudo install -o root -g root -m 0644 marchi.service /etc/systemd/system/marchi.service
sudo systemctl daemon-reload
sudo systemctl enable --now marchi
```

Check it's up:

```bash
sudo systemctl status marchi
curl -k https://127.0.0.1:8080/
```

First run without `marchi.env` needs a one-time password setup: open the
URL above in a browser, it'll prompt to set a Master Key password
(≥12 characters) the same way the CLI does on first run.

## Verifying graceful shutdown

`systemctl stop`/`restart` sends SIGTERM, which Marchi already handles
(in-flight writer transactions drain, WAL checkpoints, 30s watchdog) —
the same shutdown path every other way of running Marchi uses. Nothing
systemd-specific to configure here; `journalctl -u marchi` after a
restart should show a clean shutdown log line, not a killed-mid-write one.

```bash
sudo systemctl restart marchi
journalctl -u marchi -n 20 --no-pager
```
