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
