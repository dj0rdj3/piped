# syntax=docker/dockerfile:1

ARG GO_VERSION=1.27.4
ARG NODE_VERSION=24.22.2

FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build && npm run check:production

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
COPY scripts/govulncheck.version ./scripts/govulncheck.version
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/embed.go ./web/embed.go
COPY --from=frontend /src/web/dist/ ./web/dist/
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS="$TARGETOS" GOARCH="$TARGETARCH" GOARM="${TARGETVARIANT#v}" \
    go build -buildvcs=false -trimpath -o /out/piped ./cmd/piped
RUN mkdir -p /runtime/var/lib/piped/data /runtime/tmp \
    && chown -R 10001:10001 /runtime/var/lib/piped \
    && chmod 0700 /runtime/var/lib/piped /runtime/var/lib/piped/data \
    && chmod 1777 /runtime/tmp

# Make bypasses this stage's result cache so each release gets a fresh scan.
FROM build AS checked
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go run golang.org/x/vuln/cmd/govulncheck@$(cat scripts/govulncheck.version) -mode=binary /out/piped

# Export just the standalone binary with --target binary --output type=local,dest=bin.
FROM scratch AS binary
COPY --from=checked /out/piped /piped

# The default build produces the production runtime image.
FROM scratch AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo/ /usr/share/zoneinfo/
COPY --from=build /runtime/ /
COPY --from=checked /out/piped /usr/local/bin/piped
WORKDIR /var/lib/piped
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/piped"]
