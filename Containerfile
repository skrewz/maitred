# ---------------------------------------------------------------------------
# maitred — Minimal Containerfile
# ---------------------------------------------------------------------------
#
# Build:  podman build -t maitred .
# Run:    podman run -d --name maitred \
#           -p 9090:9090 \
#           -v /path/to/config:/etc/maitred:Z,ro \
#           -v /path/to/data:/var/lib/maitred:Z \
#           maitred:latest
# ---------------------------------------------------------------------------
FROM golang:1.26-trixie AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
ARG LDFLAGS="-s -w"
RUN CGO_ENABLED=0 go build -trimpath -ldflags "${LDFLAGS}" -o /usr/local/bin/maitred ./cmd/maitred

# ---------------------------------------------------------------------------
# runtime — minimal Debian image
# ---------------------------------------------------------------------------
FROM debian:12-slim

RUN groupadd -r maitred && useradd -r -g maitred -d /var/lib/maitred -s /sbin/nologin maitred

COPY --from=builder /usr/local/bin/maitred /usr/local/bin/maitred

RUN mkdir -p /var/lib/maitred && chown -R maitred:maitred /var/lib/maitred

WORKDIR /var/lib/maitred

USER maitred

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD /usr/local/bin/maitred --health || exit 1

ENTRYPOINT ["/usr/local/bin/maitred"]
CMD ["--trigger-dir", "/etc/maitred/triggers.d", "--data-dir", "/var/lib/maitred"]
