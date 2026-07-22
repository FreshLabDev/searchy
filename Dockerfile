# ---- build stage ----
FROM golang:1.26.5-alpine AS build
WORKDIR /src

# Cache deps first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Stamp build metadata into the binary (branchy-style) for logs, /healthz and
# the /about screen. Defaults to the current product version; CI overrides
# VERSION/COMMIT/DATE from the git tag.
ARG VERSION=0.2.0-alpha.1
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X searchy/internal/buildinfo.Version=${VERSION} \
      -X searchy/internal/buildinfo.Commit=${COMMIT} \
      -X searchy/internal/buildinfo.Date=${DATE}" \
    -o /out/searchy ./cmd/bot

# ---- runtime stage ----
# distroless/static ships CA certs (needed for HTTPS to api.telegram.org) and
# nothing else — tiny and no shell.
FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=0.2.0-alpha.1
LABEL org.opencontainers.image.version=${VERSION}
COPY --from=build /out/searchy /usr/local/bin/searchy

EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/searchy"]
