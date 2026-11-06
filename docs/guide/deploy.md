# Deploy Forgebase on a VM

This guide puts Forgebase on one small virtual machine with Docker: PostgreSQL, the web server, a background worker and Caddy, which gets and renews the HTTPS certificate by itself. It works on Oracle Cloud Always Free and on any Ubuntu 22.04 or 24.04 VM with 1 GB of RAM or more. The files are in [`deploy/`](../../deploy/) and the [`Dockerfile`](../../Dockerfile). For what the pieces do and how to run them without Docker, see [Operating Forgebase](operations.md).

What was checked: the image, the single-container mode, the compose file, a backup and a restore were run on a Linux machine with Docker (amd64). What was **not** run for this guide: a real VM, Let's Encrypt and Oracle Cloud. The steps for those come from their documentation; the first person to follow them should report any difference.

## What you get

```
Internet --80/443--> caddy (HTTPS, Host passed through)
                        |
                        v  http, private network
                    forgebase (serve, port 8000)     worker (jobs and schedules)
                        \                               /
                         `-------> postgres 16 <-------'
                                   (volume pgdata; no TLS: private network)
```

- `https://<your domain>/desk/` is the web interface, `https://<your domain>/api/v1/...` the API.
- The image runs the **`sales` example app** by default, so you have DocTypes to try. To run your own app, build a workspace binary of yours (`FORGEBASE_BIN`, see [Your first app](first-app.md)).
- PostgreSQL is reachable only on the private Docker network, so its connection needs no TLS (a URL without `sslmode` means no TLS; for a database on another network see [Render + Neon](#3-render--neon-free-path) and [TLS to PostgreSQL](operations.md#tls-to-postgresql)). Only ports 80 and 443 are open.
- Free-tier notes: Oracle Cloud's Always Free tier included, at the time of writing, Ampere A1 (arm64) instances with up to 4 OCPUs and 24 GB of memory in total, and two small AMD micro instances with 1 GB (verify the current limits on Oracle's site; free tiers change). The image is built on the VM itself (`docker compose build`), so there is no cross-building: every base image is multi-arch and works on arm64 and amd64.

## 1. The VM path

### Create the VM and open the ports

Any Ubuntu 22.04 or 24.04 VM with a public IP address works. On Oracle Cloud choose Ubuntu and, if you can, the Ampere A1 shape with 2 OCPUs and 12 GB or more; log in over SSH as `ubuntu`.

Open ports **80 and 443** (TCP) to the internet:

- **On OCI**, twice: first in the VCN's **security list** (Networking, your VCN, the subnet's security list, add ingress rules for source `0.0.0.0/0`, TCP, destination ports 80 and 443), then **inside the instance**, because Ubuntu images on OCI ship restrictive iptables rules:

  ```sh
  # on OCI only
  sudo iptables -I INPUT 6 -m state --state NEW -p tcp --dport 80 -j ACCEPT
  sudo iptables -I INPUT 6 -m state --state NEW -p tcp --dport 443 -j ACCEPT
  sudo apt-get install -y iptables-persistent netfilter-persistent
  sudo netfilter-persistent save
  ```

  (If `iptables -L INPUT --line-numbers` shows a final REJECT rule at a different position, insert before it: the new rules must come before the REJECT.)
- **Elsewhere**: open 80 and 443 in the provider's firewall or security group; with `ufw`, `sudo ufw allow 80,443/tcp`.

### A domain name

Point a DNS **A record** (and `AAAA` if the VM has an IPv6 address) for a name such as `forgebase.example.com` at the VM's public IP, and wait until `dig +short forgebase.example.com` shows it. Caddy needs this name to get a certificate, and Forgebase accepts requests only for it.

**No domain?** Caddy cannot get a certificate for a bare IP address. Options:

- Use a free DNS name service of your choice that gives you a name pointing at your IP, then proceed as above.
- **Insecure test only:** serve plain HTTP on the IP. In `.env` (below) set `FORGEBASE_DOMAIN=http://<ip>`, `FORGEBASE_ALLOWED_HOSTS=<ip>` and `FORGEBASE_DEV_INSECURE_COOKIES=1`. The session cookie then has no `Secure` flag and passwords cross the Internet unencrypted: use it only on a throw-away VM with data you do not mind losing, and never for real users.

### Install Docker

From Docker's official instructions for Ubuntu (see <https://docs.docker.com/engine/install/ubuntu/>):

```sh
sudo apt-get update
sudo apt-get install -y ca-certificates curl git
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}") stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo usermod -aG docker "$USER"    # log out and back in for this to apply
docker compose version
```

### A swap file on a small VM

The Rust release build can need more than 1 GB of memory and fail with "killed" or "out of memory". On a 1 GB VM add swap first (not needed on an A1 shape with 6 GB or more):

```sh
sudo fallocate -l 4G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
```

The first build takes several minutes (about 10 to 20 on a small VM; it compiles every dependency) and needs a few GB of disk. Later builds reuse Docker's cache.

### Get the code and configure

```sh
git clone https://github.com/tvstore/Forgebase
cd Forgebase/deploy
cp .env.example .env
chmod 600 .env
sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$(openssl rand -hex 24)/" .env
nano .env        # set FORGEBASE_DOMAIN to your name
sudo install -d -o 10001 -g 10001 -m 700 /var/backups/forgebase   # for the backups, see below
```

`.env` is private: it holds the database password. It is ignored by git; never commit it.

### Start it

```sh
docker compose up -d --build
docker compose run --rm cli migrate
printf '%s\n' 'choose-a-long-password' | docker compose run --rm -T cli user create admin --role Administrator
```

(`migrate` creates the tables and, for `sales`, the example data. Run `migrate` again after every upgrade. The password is read from standard input, never from an argument; the usernames allow letters, digits and `_ . @ -`, and passwords need 8 to 1024 bytes.)

Open **`https://<your domain>/desk/`** and log in as `admin`. The first request makes Caddy fetch the certificate, which can take a few seconds; if it fails, check that the DNS name resolves to the VM and that ports 80 and 443 are open, then `docker compose logs caddy`.

### Operate it

```sh
docker compose ps                  # state of the services
docker compose logs -f forgebase worker      # logs (also: caddy, postgres)
docker compose restart forgebase   # restart one service
docker compose down                # stop everything (data stays in the volumes)
```

**Upgrade** (see also [Upgrading](operations.md#upgrading) for the general rules):

```sh
cd Forgebase && git pull
cd deploy
# take a backup first (below)
docker compose build
docker compose stop forgebase worker
docker compose run --rm cli migrate
docker compose up -d
```

### Backups and restores

`site backup` runs `pg_dump` (version 16 is in the image, matching the server) and writes a file that holds everything, **password hashes and sessions included**: keep the directory private and copy the files off the VM. Run it daily from the **root** crontab (`sudo crontab -e`), because the backup directory belongs to uid 10001 (the container user) with mode 700:

```cron
15 3 * * * cd /home/ubuntu/Forgebase/deploy && docker compose run --rm -T cli site backup default --out /backups/forgebase-$(date +\%Y\%m\%d-\%H\%M\%S).dump >> /var/log/forgebase-backup.log 2>&1
45 3 * * * find /var/backups/forgebase -name 'forgebase-*.dump' -mtime +7 -delete
```

(Adjust the path to where you cloned the repository. `%` must be written `\%` in a crontab. The second line is the retention: it keeps seven days. A backup never overwrites a file, which is why the name has the time.) Run the backup command once by hand and check that the file appears.

**Restore.** A restore goes into an **empty** database ([details](operations.md#restoring)). On a new stack the database `forgebase` is already empty. To replace the data of a running site, stop the services and recreate the database; this **deletes the current data**:

```sh
cd Forgebase/deploy
docker compose stop forgebase worker
docker compose exec postgres psql -U forgebase -d postgres -c 'DROP DATABASE forgebase' -c 'CREATE DATABASE forgebase'
docker compose run --rm cli site restore default --from /backups/forgebase-20261031-031500.dump
docker compose run --rm cli migrate        # does nothing if the binary and the backup match
docker compose up -d
```

Test a restore on a spare VM or stack before you need it.

## 2. Single-container mode

`all` runs the web server **and** the worker in one container, which saves a container and some memory. Use it on a very small machine or on a host that runs only one container. In `compose.yaml` give `forgebase` the command `["all"]` and remove the `worker` service, or without compose. This example is meant to sit behind an HTTPS reverse proxy (the session cookie is `Secure`); for a local test over plain HTTP add `-e FORGEBASE_DEV_INSECURE_COOKIES=1` and `FORGEBASE_ALLOWED_HOSTS=localhost` (insecure, testing only):

```sh
docker run -d --name forgebase --restart unless-stopped -p 8000:8000 \
  -e FORGEBASE_DATABASE_URL=postgres://user:password@dbhost:5432/forgebase \
  -e FORGEBASE_ALLOWED_HOSTS=forgebase.example.com \
  -e FORGEBASE_MIGRATE_ON_START=1 \
  forgebase-sales all
```

How it behaves (`deploy/entrypoint.sh`):

- It starts `serve` and `worker`, forwards every SIGTERM or SIGINT it receives to both (one signal makes them stop gracefully; a second one makes them exit at once), waits for both and exits with 0 after a requested stop, unless a process fails while stopping (then with its status; 143 is possible after a forced double stop). If one process exits on its own, it sends the other one SIGTERM and exits with the first one's status (also when a signal arrives meanwhile), so the container restarts (`restart: unless-stopped`) as a unit.
- `FORGEBASE_MIGRATE_ON_START=1` runs `migrate` before `serve` or `all` start (default **off**). A failed migration stops the container with a non-zero exit code. It is convenient on a host without a shell, but two containers that start together both migrate; use it with one instance only.
- `FORGEBASE_LISTEN`, if unset, is `0.0.0.0:$PORT` when `PORT` is set (Render and similar hosts) and `0.0.0.0:8000` otherwise.
- In `all` mode the script is PID 1 and reaps its children itself, so `--init` is not needed; in the other modes the binary is PID 1 (compose sets `init: true`; with `docker run`, `--init` is optional).
- Trade-offs: a crash of either process restarts both; a busy worker and the web server share one memory budget; stopping gives running jobs up to 30 seconds, so use `docker stop -t 45` (compose: `stop_grace_period: 45s`); the logs of both go to one stream. The separate layout above is the recommended one for production.

## 3. Render + Neon (free path)

A free web service on [Render](https://render.com) with a free PostgreSQL database on [Neon](https://neon.com). Forgebase connects to Neon over TLS with `sslmode=require` ([ADR 0018](../adr/0018-postgres-tls.md)); no code change or extra feature is needed.

**What was checked, and what was not.** Checked here: the TLS connection, with certificate verification, against a PostgreSQL 16 with TLS and a private CA; `migrate`, `user create`, `site backup` and `site restore` over TLS; the image in `all` mode against that server with `sslmode=require` and `sslrootcert` (it migrates, serves, logs in, and `site backup` runs `pg_dump` with `verify-full`); and that the image has a CA store (the Debian bundle, 148 roots, which the Dockerfile copies from its build stage: the `postgres` base image has none) that a certificate from a public CA, like Neon's, is checked against. That a Neon certificate really verifies against this bundle could not be tried here. **Not checked**, because it needs accounts: a real Neon project and a real Render service. Every provider detail below (screens, names, limits, prices, sleep times) is from memory and may be out of date: **check the provider's current documentation** for each one. If a step differs, report it.

The path in short: a Neon database, the first Administrator created from your own computer, a Render web service built from this repository's `Dockerfile`.

### 3.1 Create the Neon database

1. Create a Neon project on the free plan (check Neon's current docs for the plan's limits and for how to get a connection string). **Choose PostgreSQL 16** as the version: the image's `pg_dump` and `pg_restore` are version 16 and must match the server's major version, and Neon's docs say whether the version of a project can be changed later (check Neon's docs; assume it cannot).
2. Copy the connection string of the **direct** (non-pooled) connection: the host does **not** contain `-pooler`. In Neon's connection dialog, turn "Connection pooling" off.
3. Make it a URL that ends with `?sslmode=require`:

   ```
   postgres://USER:PASSWORD@ep-example-123456.REGION.aws.neon.tech/neondb?sslmode=require
   ```

   If Neon's string also has `&channel_binding=require`, you can leave it (the connection supports it) or remove it; `site backup` and `site restore` refuse that option, so remove it if you plan to take backups with Forgebase (see [Backups](operations.md#backups)). `sslmode=require` makes Forgebase verify Neon's certificate and the host name against the operating system's CA store, which is what you want: no `sslrootcert` is needed.

**Why the direct endpoint.** Neon's pooled endpoint (`-pooler`) uses PgBouncer in transaction mode (check Neon's docs). `migrate` takes a **session-level advisory lock** and a session-level `lock_timeout` on one connection and holds them for the whole run; a transaction-mode pooler may serve the next statement from another server connection, which breaks both. The rest of Forgebase uses transaction-scoped settings (`SET LOCAL`, `set_config(..., true)`) and `FOR UPDATE SKIP LOCKED` inside transactions, which are fine through a pooler, but the safe choice is the direct endpoint everywhere: a small free app does not need pooling. Keep the URL secret: it holds the password. Never commit it; it lives only in your shell and in Render's secret environment.

### 3.2 Migrate and create the first Administrator, from your computer

Render's free tier has no shell, so create the schema and the first Administrator **before** the first deploy, against the Neon URL, from a clone of this repository (Rust installed, see the [README](../../README.md#quick-start-in-5-minutes)). There is no bootstrap feature: this is the normal `user create` command.

```sh
git clone https://github.com/tvstore/Forgebase && cd Forgebase
read -rs -p 'Neon URL: ' FORGEBASE_DATABASE_URL && export FORGEBASE_DATABASE_URL; echo   # paste the URL; it is not echoed or stored
cargo run -p sales -- migrate
read -rs -p 'Administrator password (8 or more characters): ' PW; echo
printf '%s\n' "$PW" | cargo run -p sales -- user create admin --role Administrator
unset PW FORGEBASE_DATABASE_URL
```

(The password is read from standard input, never from an argument. Without Rust, build the image locally and run the same commands in it: `docker build -t forgebase-sales .`, then `docker run --rm -i -e FORGEBASE_DATABASE_URL forgebase-sales migrate` and `printf '%s\n' "$PW" | docker run --rm -i -e FORGEBASE_DATABASE_URL forgebase-sales user create admin --role Administrator`, with the variable exported as above.)

### 3.3 Deploy on Render

1. In Render, create a **Web Service** from the public GitHub repository `https://github.com/tvstore/Forgebase` (check Render's current docs for connecting a public repository).
2. Runtime: **Docker**, using the repository's `Dockerfile` (the default path). Instance type: **Free**.
3. Environment variables:

   | Name | Value |
   |---|---|
   | `FORGEBASE_DATABASE_URL` | the Neon URL of step 3.1, entered as a **secret** (a secret environment variable, never in a committed file) |
   | `FORGEBASE_ALLOWED_HOSTS` | `<your-service>.onrender.com` (the name Render gives the service, without `https://`) |
   | `FORGEBASE_MIGRATE_ON_START` | `1` (runs `migrate` on every start; it does nothing when the database is current) |

   `PORT` is set by Render, and the image's entrypoint listens on it (`0.0.0.0:$PORT`). Do not set `FORGEBASE_DEV_INSECURE_COOKIES`: Render serves HTTPS and the session cookie must stay `Secure`.
4. Docker command (Render's "Docker Command" field, which becomes the arguments of the image's entrypoint): **`all`**. This is the single-container mode ([section 2](#2-single-container-mode)): the web server and the job worker run in one container, which is what a free service allows.
5. Deploy. Open `https://<your-service>.onrender.com/desk/` and log in as `admin` with the password of step 3.2.

If the service does not start, read Render's logs. A line that starts with `error performing TLS handshake` or `no root certificate` is a connection problem: see [TLS to PostgreSQL](operations.md#tls-to-postgresql) and the troubleshooting table there.

### 3.4 Limits to expect (check each provider's current docs)

- **The service sleeps** after about 15 minutes without requests and takes about a minute to start again. The first request after a sleep is slow.
- **Jobs and schedules run only while the service is awake.** A scheduled job that is due while it sleeps runs late, when the next request wakes it. This is not a place for time-critical work.
- **Free-hours and bandwidth limits** apply to the Render service (monthly instance hours) and to Neon (storage and compute hours); a service that runs out is suspended until the limit resets or you upgrade.
- **Neon's free compute suspends** when idle, so the first query after a pause is slower (a second or more).
- **The Rust build on Render's builder may be slow**, ten minutes or more, and a small builder can run out of memory (the same problem as the small VM above). If it fails, build the image on your computer and deploy it from a registry instead (check Render's docs for deploying a prebuilt image).
- **Backups.** Neon's free plan has limited point-in-time history (check Neon's docs). To take your own backups, run `site backup` from your computer against the Neon URL (it needs the PostgreSQL 16 client tools, and the Neon project must be PostgreSQL 16: see step 3.1, check Neon's docs) and keep the file private.

## 4. Security notes

- **Secure cookies stay on behind HTTPS**: never set `FORGEBASE_DEV_INSECURE_COOKIES` in production.
- **The database URL is a secret** (it holds the password): in Render it is a secret environment variable, on your computer a shell variable that you `unset` afterwards. Over any network that is not private use `sslmode=require` in it.
- **Allowed hosts**: `FORGEBASE_ALLOWED_HOSTS` is the list of host names Forgebase answers to (compose sets it to `FORGEBASE_DOMAIN`). Caddy passes the `Host` header unchanged; do not rewrite it.
- **Keep `.env` private** (`chmod 600`, never commit it, never paste it into an issue). The database password is in it.
- Only 80 and 443 are open; PostgreSQL and the application port are not published to the host.
- Backups hold password hashes and sessions: keep them private and encrypt them where you store them off the VM.
- Keep the VM updated (`sudo apt-get upgrade`) and update the image regularly (`git pull`, build): see [the maintenance policy](../blueprint/maintenance-policy.md#container-images) for how the base images are pinned.
- More: the [security model](../blueprint/security-model.md).
