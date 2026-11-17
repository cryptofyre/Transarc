<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.png">
  <img alt="Transarc" src="assets/logo.png" width="420">
</picture>

Transarc is a video library compressor. It takes a library of video you already have and compresses it to AV1, in the same folders, without building a second collection beside it.

A new file replaces the original only when it is worth keeping. By default that means at least 10% smaller, and the same length. Audio, subtitles, and chapters stay with the file.

## Compressing a library

You add one or more folders. The biggest files go first. You leave anything that is already AV1, and anything too small to be worth the time. A file that is still being copied waits until later.

Each file starts with a 30 second sample, so you can see whether a full compress is likely to save enough space. If it isn't, you keep the original and don't try that file again for 21 days. If it is, you compress the whole file. With more than one graphics card, you compress more than one file at a time, and a free card takes the next file.

You only swap the original out once the new file is small enough and the same length. If the swap is interrupted, you still have the original.

A dry run lists what would change and writes nothing. You can run a pass whenever you want, or leave the service on a schedule. The example schedule is once a day, at 3:00 in the morning, in whatever timezone you set. If a pass is still going when the next one is due, that start waits.

Dolby Vision files with no normal HDR picture underneath are left alone. The others are compressed from that normal picture, without the extra Dolby Vision layer. If an HDR file would come out as plain 8-bit, you keep the original.

## While a file is open

If you run Plex or Jellyfin, connect it and you won't compress a file someone has open, pause included. When they stop, that file continues. The rest of the library keeps going, and the wait is not saved as a skip.

For Plex, set the server address and mount `Preferences.xml` read-only. The token is read from that file. You don't put it in the config, and it doesn't show up in the log.

For Jellyfin, set the server address. You can supply an API key, or let one be read from Jellyfin. A key named `transarc` is preferred.

Those servers often use different folder paths than you mounted here. `path_map` is how you match them. If a server doesn't answer, you get one warning and compression continues.

## Sonarr and Radarr

You don't need either one. The library is just the folders you mounted. If Sonarr or Radarr already manages a folder, add it and that app refreshes the folder after you replace a file, so it learns the new size.

## Hardware

Intel Arc cards are the fast path. Pass them into Docker and each card compresses its own file. The image includes the FFmpeg build Arc needs.

NVIDIA works as well, with its own quality setting. That number is not the Arc number. The CPU setting is a third number again, for a machine or a Raspberry Pi with no AV1 encoder. CPU compression is one file at a time. A Pi is reasonable for 1080p. A 4K file can take hours and finish on a later run. Use a 64-bit system for the Pi image.

Leave the backend on automatic and you get Arc if the cards are there, NVIDIA if that's what you have, and the CPU otherwise. Set it to Arc when you always want the cards, so a missing GPU doesn't move the work onto the CPU.

## Install

Copy `config.example.yaml`, or run `transarc init`, and list your library folders. Sonarr, Radarr, Plex, and Jellyfin are optional.

```bash
docker build -t transarc .
docker run -d --name transarc --restart unless-stopped \
  --network host \
  --device /dev/dri \
  -e TZ=Your/Timezone \
  -v /path/to/config:/config \
  -v /path/to/temp:/temp \
  -v /path/to/library:/libraries/media \
  transarc service
```

Mount every library folder you listed in the config. `/dev/dri` passes Intel graphics through. Drop it, and set the backend to CPU, when you have no card to use. Host networking matters when Sonarr, Radarr, Plex, or Jellyfin are on the same machine.

On Unraid, `deploy/unraid/transarc.xml` and `deploy/unraid/install.sh` are a starting point. Change the folders and timezone to yours. The script writes a config only when you don't have one, and it doesn't print API keys. `PUID` 99 and `PGID` 100 match the usual Unraid user and group.

Images are built for PCs and for Raspberry Pi. Pull `ghcr.io/cryptofyre/transarc:latest`.

Check the cards before you compress anything:

```bash
docker run --rm --network host --device /dev/dri \
  -v /path/to/config:/config \
  transarc devices
```

`docker logs -f transarc` shows the next run, how much of the library is queued, and progress from each card.

Inside the container, `transarc plan` prints that plan, and `transarc start` begins a pass before the schedule. From the host that is `docker exec transarc transarc plan` and `docker exec transarc transarc start`. The pass logs stay with the service.

## Commands

```text
transarc service     keep running and compress on your schedule
transarc plan        show the plan, including what is queued
transarc start       start a pass now, without waiting for the schedule
transarc run         compress the library now
transarc scan        show what the next pass would look at
transarc file PATH   compress one file, and keep it only when you save space
transarc devices     check that your graphics cards come up
transarc status      what you replaced, what you kept, and how much space you saved
transarc init        write a starter config
```

Set `sample_seconds` to `-1` when you want every file fully compressed, with no sample first. Leave `ffmpeg.allow_upstream` off unless you mean to try a different FFmpeg build.

## HTTP API

A dashboard can read what the service is doing over HTTP. It is off until you set `api.listen`, or `TRANSARC_API_LISTEN`, to a `host:port` such as `:8790`. The API only reads. It cannot start a pass or change anything.

With host networking, `:8790` listens on every network the server is on. Set `api.token`, or `TRANSARC_API_TOKEN`, and send it as `Authorization: Bearer <token>`. You can also bind a single address, such as `10.10.79.15:8790`. The responses include library file paths. Everything except `/api/v1/health` needs the token once one is set.

If the port is taken, you get one warning and compression carries on without the API.

History comes from the state database, which keeps one row per file: its latest decision. Live jobs, progress, and events are held in memory only. A restart starts them empty, and events from before the restart are not replayed.

| Method and path | What you get |
| --- | --- |
| `GET /api/v1/health` | `{"ok": true, "version": "0.1.0"}`, with no token needed |
| `GET /api/v1/status` | The pass, the files encoding now, all-time totals, and the last library count |
| `GET /api/v1/history?limit=50&before=<unix>&decision=encoded,kept,failed` | Latest decision per file, newest first. `decision` also takes `skipped`, `queued` or `all`. Pass `next_before` back as `before` for the next page |
| `GET /api/v1/queue?limit=50` | Files a prescan marked as work, largest first |
| `GET /api/v1/events` | A Server-Sent Events stream |

`/api/v1/status`:

```json
{
  "version": "0.1.0",
  "backend": "qsv",
  "devices": [{ "id": "D128", "node": "renderD128", "name": "Intel Arc A750" }],
  "pass": {
    "running": true,
    "started": "2026-11-16T09:00:00Z",
    "info": { "candidates": 40, "set_aside": 860, "devices": ["D128", "D129"], "dry_run": false },
    "next": "2026-11-17T09:00:00Z"
  },
  "jobs": [{
    "path": "/libraries/movies/Scream 7 (2026)/Scream 7 (2026).mkv",
    "name": "Scream 7 (2026).mkv",
    "device": "D128",
    "phase": "encode",
    "percent": 34.9, "fps": 75, "speed": 3.1, "out_bytes": 2362232012,
    "source_bytes": 49392123904,
    "codec": "hevc", "width": 3840, "height": 2160, "hdr": "HDR10",
    "sample_savings_pct": 81.2,
    "started": "2026-11-16T19:31:02Z", "updated": "2026-11-16T20:12:17Z"
  }],
  "totals": { "encoded": 96, "kept": 0, "skipped": 9, "failed": 8, "saved_bytes": 4800000000000 },
  "queue": {
    "finished": "2026-11-16T08:42:10Z",
    "files": 3378, "candidates": 40, "skipped": 3330, "settling": 0, "failed": 8,
    "reasons": [{ "reason": "already av1", "n": 2900 }]
  },
  "time": "2026-11-16T20:12:18Z"
}
```

`phase` is `sample`, `encode` or `paused`. A paused file is open in Plex or Jellyfin. `totals` and `queue` are `null` when there is nothing to report yet.

`/api/v1/history`:

```json
{
  "items": [{
    "path": "/libraries/movies/Masters of the Universe (2026)/Masters of the Universe (2026).mkv",
    "name": "Masters of the Universe (2026).mkv",
    "decision": "encoded",
    "reason": "savings 77.9%",
    "size": 11059540787,
    "source_bytes": 50251117363,
    "output_bytes": 11059540787,
    "saved_bytes": 39191576576,
    "savings_pct": 77.9,
    "device": "D129",
    "checked": "2026-11-16T20:04:01Z"
  }],
  "next_before": 1794859441
}
```

The event stream starts with a `status` event, the same document as `/api/v1/status`, and then sends events as they happen. Each stored event has an `id`. Reconnect with `Last-Event-ID`, or `?since=<id>`, to replay the last 256 events you missed. A comment line is sent every 15 seconds to keep the connection open.

```text
id: 412
event: job.finish
data: {"id":412,"type":"job.finish","time":"2026-11-16T20:04:01Z","path":"/libraries/movies/…/Masters of the Universe (2026).mkv","name":"Masters of the Universe (2026).mkv","data":{"outcome":"encoded","reason":"savings 77.9%","source_bytes":50251117363,"output_bytes":11059540787,"savings_pct":77.9,"device":"D129","duration_sec":2274}}
```

| Event | `data` |
| --- | --- |
| `pass.start` | `candidates`, `set_aside`, `devices`, `dry_run` |
| `pass.finish` | `encoded`, `kept`, `skipped`, `failed`, `saved_bytes` |
| `job.start` | the job, as in `status.jobs` |
| `job.phase` | `phase` |
| `job.sample` | `savings_pct`, `worth` |
| `job.progress` | the job. At most one a second for each file, with no `id`, and never replayed |
| `job.finish` | `outcome` (`encoded`, `kept`, `failed`, `skipped` or `canceled`), `reason`, `source_bytes`, `output_bytes`, `savings_pct`, `device`, `duration_sec` |
| `prescan` | `files`, `candidates`, `skipped`, `settling`, `failed` |
