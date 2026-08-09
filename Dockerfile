FROM python:3.12-slim-bookworm

ARG QUOTO_VERSION=0.10.2
ARG QUOTO_REVISION=unknown

LABEL org.opencontainers.image.title="Quoto" \
      org.opencontainers.image.version="${QUOTO_VERSION}" \
      org.opencontainers.image.revision="${QUOTO_REVISION}"

ENV QUOTO_VERSION=${QUOTO_VERSION}

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY . .

# Run as a non-root user; own /app (incl. the logs dir backing the volume).
RUN useradd --create-home --uid 1000 quoto \
    && mkdir -p /app/logs \
    && chown -R quoto:quoto /app
USER quoto

# Liveness: heartbeat is refreshed by the scheduler; stale => loop is wedged.
HEALTHCHECK --interval=30s --timeout=10s --start-period=40s --retries=3 \
    CMD ["python", "/app/healthcheck.py"]

CMD ["sh", "/app/docker-entrypoint.sh"]
