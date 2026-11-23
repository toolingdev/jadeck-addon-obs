# OBS Studio — jadeck add-on

OBS does the video (captures, layout, streaming to YouTube); jadeck is the
remote. Go live, switch scenes, show or hide lyrics, mix audio and see
what's on air, from any phone, tablet or computer on your network or
Tailscale.

Works with OBS Studio 28 or later. The add-on talks to OBS over
obs-websocket, which is built into OBS. No OBS code is included: OBS is
separate free software (GPL-2.0) that you install yourself.

## Quick setup

1. Install OBS Studio: <https://obsproject.com/download>.
2. In OBS: **Tools → WebSocket Server Settings**, tick **Enable WebSocket
   server**, click **Show Connect Info** and copy the password.
3. In jadeck: **Settings → Connections → Add → OBS Studio**. OBS on the
   same computer: leave 127.0.0.1. Paste the password, press **Test**.
4. Optional: press **Make the scenes for me** (below).

### Lyrics from Proclaim

1. Install the DistroAV plugin for OBS (NDI):
   <https://github.com/DistroAV/DistroAV/releases>.
2. In Proclaim, turn on NDI output with a transparent background.
3. In OBS, add an NDI source named **Proclaim** that shows it (or let
   Make the scenes do it, then pick Proclaim's output in the source).

### A sermon over Zoom

Join the call, pin the speaker, hide your own video and people without
video. In OBS, capture the Zoom app (on a Mac, OBS also captures its
sound on macOS 13 or later). Make the scenes does this for you.

## Make the scenes for me

Adds these scenes to OBS, using your Zoom capture and Proclaim source
(made if missing). Scenes that already exist are left alone; pressing it
again never makes duplicates.

| Scene | What's in it |
|---|---|
| Starting soon | A dark card: "We'll begin shortly" |
| Pastor | The Zoom call, full frame |
| Pastor + lyrics | The Zoom call with Proclaim's lyrics on top |
| Slide full screen | Proclaim on a dark background (the call stays underneath, so the sermon is still heard) |
| Be right back | A card, for when the call drops (the call's sound stays on) |
| Ending | "Thank you for joining us" |

## Widgets

| Widget | What it does |
|---|---|
| Program | A live picture of what's on air (OBS's own render), with LIVE and REC badges and the scene's name. 0.5-2 pictures a second. |
| Stream | Go live / Stop stream and Record, pressed and held (3-10 s, default 5). How long you've been live, bitrate, dropped frames and CPU, and what to do when the upload struggles. |
| Scenes | OBS's scenes as buttons: red is on air (green is next in Studio Mode). Pick and order them, or show all. |
| Lyrics | Show or hide the lyrics source in the scene on air; switch between the lower-third and full-screen-slide scenes. |
| Audio | A fader, LED meter and mute for each source with sound (the shared jadeck strip). |

OBS is also a **switcher** in jadeck: its scenes are the inputs, so the
built-in Cut / Fade, Input bank, Program and the stage screen's
"We are live" work with OBS. Outside Studio Mode, the "preview" is kept
by jadeck until you press Cut or Fade.

## Actions

`scene`, `transition`, `stream` and `record` (need `confirmed: true`),
`lyrics`, `mute`, `volume`, `make_scenes`. Each is a dashboard button, a
macro step and an assistant tool.

## Read-only

Tick **Read-only** (advanced) for a screen that should only watch: every
button is greyed out and the add-on refuses changes.

## Build and test

```bash
go test -race ./...
go build -o jadeck-addon-obs .
./jadeck-addon-obs --manifest > addon.json
```

The tests run against a fake OBS (`fake_test.go`) that speaks
obs-websocket v5, password included.

## OBS's license

OBS Studio is GPL-2.0 and is not part of this add-on: the add-on only
speaks OBS's remote-control protocol to a copy of OBS you install.
