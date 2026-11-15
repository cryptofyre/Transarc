FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
WORKDIR /src
ARG TARGETOS=linux
ARG TARGETARCH=amd64
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -X github.com/cryptofyre/transarc/internal/buildinfo.Version=0.1.0" \
    -o /out/transarc ./cmd/transarc

# jellyfin/jellyfin ships jellyfin-ffmpeg, which carries the Intel Arc QSV
# patches upstream FFmpeg still does not. The media driver userspace is in
# the same image, so the container only needs the host's /dev/dri nodes.
FROM jellyfin/jellyfin:latest
USER root
COPY --from=build /out/transarc /usr/local/bin/transarc
ENV TRANSARC_CONFIG=/config/config.yaml \
    TRANSARC_FFMPEG=/usr/lib/jellyfin-ffmpeg/ffmpeg \
    TRANSARC_FFPROBE=/usr/lib/jellyfin-ffmpeg/ffprobe \
    PUID=99 \
    PGID=100 \
    TZ=America/Chicago
# The Jellyfin base image checks its own web port. This process is not Jellyfin.
HEALTHCHECK --interval=60s --timeout=15s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/transarc", "health"]
ENTRYPOINT ["/usr/local/bin/transarc"]
CMD ["service"]
