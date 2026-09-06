# syntax=docker/dockerfile:1
# Build context is the repository root.
#
# Deliberately FROM the same image the database server runs. pg_dump "cannot
# dump from PostgreSQL servers newer than its own major version; it will
# refuse to even try", so deriving from the server's own image makes
# "pg_dump is never older than the server" true by construction -- a future
# Postgres major bump cannot silently leave the sidecar behind and break
# backups quietly.

FROM pgvector/pgvector:pg17

ARG RESTIC_VERSION
ARG RESTIC_SHA256
ARG AGE_VERSION
ARG AGE_SHA256
ARG SUPERCRONIC_VERSION
ARG SUPERCRONIC_SHA256

# jq shapes the /status payload out of `restic snapshots --json`; socat is the
# status server; flock serialises the daily job, the weekly copy and any manual
# run against each other; curl is the health check's own client.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl bzip2 openssh-client jq socat util-linux \
 && rm -rf /var/lib/apt/lists/*

RUN test -n "$RESTIC_VERSION" && test -n "$RESTIC_SHA256" \
 && curl -fsSL -o /tmp/restic.bz2 \
      "https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_amd64.bz2" \
 && echo "${RESTIC_SHA256}  /tmp/restic.bz2" | sha256sum -c - \
 && bunzip2 -c /tmp/restic.bz2 > /usr/local/bin/restic \
 && rm /tmp/restic.bz2 && chmod 0755 /usr/local/bin/restic

# age encrypts the application master-key backup to an owner-held recipient
# whose private identity never lives on this host.
RUN test -n "$AGE_VERSION" && test -n "$AGE_SHA256" \
 && curl -fsSL -o /tmp/age.tar.gz \
      "https://github.com/FiloSottile/age/releases/download/v${AGE_VERSION}/age-v${AGE_VERSION}-linux-amd64.tar.gz" \
 && echo "${AGE_SHA256}  /tmp/age.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/age.tar.gz -C /tmp \
 && mv /tmp/age/age /tmp/age/age-keygen /usr/local/bin/ \
 && rm -rf /tmp/age /tmp/age.tar.gz \
 && chmod 0755 /usr/local/bin/age /usr/local/bin/age-keygen

# Compose has no cron primitive, so the schedule lives inside the container.
# That way `docker compose up` brings the backup schedule with it, rather than
# leaving it in a host crontab that is easy to forget on a host rebuild.
RUN test -n "$SUPERCRONIC_VERSION" && test -n "$SUPERCRONIC_SHA256" \
 && curl -fsSL -o /usr/local/bin/supercronic \
      "https://github.com/aptible/supercronic/releases/download/v${SUPERCRONIC_VERSION}/supercronic-linux-amd64" \
 && echo "${SUPERCRONIC_SHA256}  /usr/local/bin/supercronic" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/supercronic

# The scripts (ticket #21). lib.sh is sourced by all of them and is not on PATH,
# because it is not runnable on its own.
COPY deploy/images/backup/lib.sh /usr/local/lib/vyn/lib.sh
COPY deploy/images/backup/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY deploy/images/backup/backup-daily.sh /usr/local/bin/backup-daily.sh
COPY deploy/images/backup/copy-weekly.sh /usr/local/bin/copy-weekly.sh
COPY deploy/images/backup/status-refresh.sh /usr/local/bin/status-refresh.sh
COPY deploy/images/backup/status-handler.sh /usr/local/bin/status-handler.sh
COPY deploy/images/backup/crontab /etc/vyn/crontab

# `docker compose exec backup backup-now` is how the forward-migration
# procedure takes its pre-migration backup.
RUN ln -s /usr/local/bin/backup-daily.sh /usr/local/bin/backup-now \
 && chmod 0755 /usr/local/bin/entrypoint.sh \
      /usr/local/bin/backup-daily.sh \
      /usr/local/bin/copy-weekly.sh \
      /usr/local/bin/status-refresh.sh \
      /usr/local/bin/status-handler.sh \
 && chmod 0644 /usr/local/lib/vyn/lib.sh /etc/vyn/crontab

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
