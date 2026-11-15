#!/bin/bash
# Build Transarc on the Unraid host and start the daily service.
# Existing /mnt/user/appdata/transarc/config.yaml is left alone.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
APP=/mnt/user/appdata/transarc
TMP=/mnt/cache/transarc-tmp

mkdir -p "$APP" "$TMP"

if [[ ! -f "$APP/config.yaml" ]]; then
  sonarr_key="$(sed -n 's:.*<ApiKey>\(.*\)</ApiKey>.*:\1:p' /mnt/user/appdata/sonarr-tv/config.xml)"
  radarr_key="$(sed -n 's:.*<ApiKey>\(.*\)</ApiKey>.*:\1:p' /mnt/user/appdata/radarr-normal/config.xml)"
  if [[ -z "$sonarr_key" || -z "$radarr_key" ]]; then
    echo "could not read Sonarr or Radarr API keys" >&2
    exit 1
  fi
  umask 077
  cat >"$APP/config.yaml" <<EOF
schedule: "0 3 * * *"
timezone: America/Chicago
dry_run: false
run_on_start: false
temp_dir: /temp
log_file: /config/transarc.log
state_db: /config/transarc.db

ffmpeg:
  path: /usr/lib/jellyfin-ffmpeg/ffmpeg
  probe_path: /usr/lib/jellyfin-ffmpeg/ffprobe
  allow_upstream: false

encoder:
  backend: qsv
  gpus:
    - /dev/dri/renderD128
    - /dev/dri/renderD129

rules:
  min_savings_percent: 10
  min_file_size_mb: 200
  skip_codecs: [av1]
  extensions: [mkv, mp4, m4v, avi, ts, m2ts]
  sample_seconds: 30
  sample_start_ratio: 0.2
  retry_days: 21
  settle_minutes: 15
  duration_tolerance: 0.01
  stall_minutes: 20
  probe_concurrency: 4
  skip_dolby_vision_profile5: true

qsv:
  preset: veryslow
  global_quality: 23
  look_ahead_depth: 40
  gop_seconds: 10
  b_frames: 7
  refs: 5
  low_power: true
  extbrc: true
  adaptive_i: true
  adaptive_b: true
  b_strategy: true
  bit_depth: auto

nvenc:
  preset: p7
  tune: uhq
  cq: 28
  lookahead: 32
  b_frames: 4
  aq_strength: 8
  spatial_aq: true
  temporal_aq: true
  multipass: fullres
  b_ref_mode: middle
  gop_seconds: 10
  bit_depth: auto
  lookahead_level: 3
  tf_level: 4

libraries:
  - name: tv
    path: /libraries/tv
    arr: sonarr
  - name: movies
    path: /libraries/movies
    arr: radarr

sonarr:
  url: http://127.0.0.1:8989
  api_key: "${sonarr_key}"
  path_map:
    - from: /mnt/tv
      to: /libraries/tv

radarr:
  url: http://127.0.0.1:7878
  api_key: "${radarr_key}"
  path_map:
    - from: /mnt/movies
      to: /libraries/movies
EOF
  chown nobody:users "$APP/config.yaml" || true
  echo "wrote $APP/config.yaml"
fi

docker build -t transarc:latest "$ROOT"
docker rm -f transarc >/dev/null 2>&1 || true

# The token stays in Preferences.xml. It is not passed with -e.
plex_pref="/mnt/user/appdata/binhex-plex/Plex Media Server/Preferences.xml"
plex_run=()
if [[ -f "$plex_pref" ]]; then
  plex_ip="$(docker inspect plex --format '{{range $n, $v := .NetworkSettings.Networks}}{{$v.IPAddress}}{{end}}' 2>/dev/null || true)"
  if [[ -n "$plex_ip" ]]; then
    plex_run+=(
      -e "TRANSARC_PLEX_URL=http://${plex_ip}:32400"
      -e TRANSARC_PLEX_CONFIG=/plex/Preferences.xml
      -e TRANSARC_PLEX_PATH_MAP=/tvshows/Library=/libraries/tv,/media/Library=/libraries/movies
      -v "${plex_pref}:/plex/Preferences.xml:ro"
    )
    echo "plex playback at ${plex_ip}:32400"
  fi
fi

jelly_cfg=/mnt/user/appdata/Jellyfin
jelly_run=()
if [[ -d "$jelly_cfg/data" ]] && curl -fsS -m 2 -o /dev/null "http://127.0.0.1:8096/System/Info/Public"; then
  jelly_run+=(
    -e TRANSARC_JELLYFIN_URL=http://127.0.0.1:8096
    -e TRANSARC_JELLYFIN_CONFIG=/jellyfin
    -e TRANSARC_JELLYFIN_PATH_MAP=/data/tvshows=/libraries/tv,/data/movies=/libraries/movies
    -v "${jelly_cfg}:/jellyfin:ro"
  )
  echo "jellyfin playback enabled"
fi

docker run -d \
  --name transarc \
  --restart unless-stopped \
  --network host \
  --device /dev/dri:/dev/dri \
  -e PUID=99 \
  -e PGID=100 \
  -e TZ=America/Chicago \
  -e TRANSARC_CONFIG=/config/config.yaml \
  -v "$APP":/config \
  -v "$TMP":/temp \
  -v /mnt/user/TV-Shows/Library:/libraries/tv \
  -v /mnt/user/Movies/Library:/libraries/movies \
  "${plex_run[@]}" \
  "${jelly_run[@]}" \
  transarc:latest service

echo "transarc is scheduled for 03:00 America/Chicago"
