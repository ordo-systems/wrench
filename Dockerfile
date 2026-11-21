# Wrench image: one distroless, non-root image for every service. It runs the server (default), the backups
# (`run backup`, with restic, pg_dump and pg_restore) and the development mocks (`mock llm|gitlab`).
# Build from the repository root (the build context includes the dashboard and the Go module).
#   docker build -f Dockerfile .
# The dashboard and the Go binary are built on the build machine and cross-compiled for the target platform;
# only the pgtools stage runs on the target platform (under emulation when it differs).
FROM --platform=$BUILDPLATFORM node:22.19.0-alpine@sha256:d2166de198f26e17e5a442f537754dd616ab069c47cc57b889310a717e0abbf9 AS web
WORKDIR /src/webapp
COPY webapp/package.json webapp/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY webapp/ ./
RUN mkdir -p ../internal/web/dist && npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 GOWORK=off
COPY go.mod go.sum /src/
WORKDIR /src
RUN go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=web /src/internal/web/dist/ internal/web/dist/
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/wrench \
	-ldflags "-X github.com/ordo-systems/wrench/internal/version.Version=${VERSION} -X github.com/ordo-systems/wrench/internal/version.Commit=${COMMIT}" \
	./cmd/wrench
# Distroless has no shell: create the backup state and restore directories here, owned by nonroot.
RUN mkdir -p /out/dirs/var/lib/wrench-backup/restic /out/dirs/var/lib/wrench-backup/status /out/dirs/restore

# pg_dump and pg_restore of the server's PostgreSQL major, with every shared library they load. Libraries keep
# their Debian paths (any architecture), and symlinks are copied as the files they point to.
FROM postgres:17-bookworm@sha256:665dc7b37ac52a17e298511414c21d23ffa4e6a6ef6225b3e5d59c7afb35e994 AS pgtools
RUN set -eu; mkdir -p /out/usr/local/bin; \
	for tool in pg_dump pg_restore; do \
		bin=/usr/lib/postgresql/17/bin/$tool; \
		cp "$bin" /out/usr/local/bin/; \
		for lib in $(ldd "$bin" | awk '/=> \// { print $3 } /^\t\// { print $1 }'); do \
			cp -L --parents "$lib" /out/; \
		done; \
	done

FROM restic/restic:0.20.0@sha256:93999df80d4ac6cb148e82d2ef1fe6a81996f17ff535e772723538211b31555a AS restic

FROM gcr.io/distroless/cc-debian12:nonroot@sha256:9dac0a79194e45a7da0158a9c6da57b217585af0786db3845d1f0ec1a0dd182f
COPY --from=pgtools /out/ /
COPY --from=restic /usr/bin/restic /usr/local/bin/restic
COPY --from=build --chown=65532:65532 /out/dirs/ /
COPY --from=build /out/wrench /usr/local/bin/wrench
COPY deploy/shared/config/config.yaml /etc/wrench/
ENV WRENCH_CONFIG=/etc/wrench/config.yaml
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/wrench"]
CMD ["run", "server"]
