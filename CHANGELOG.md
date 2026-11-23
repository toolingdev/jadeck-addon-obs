# Changelog

## 0.1.2

- **Scenes:** while Be right back is on air its button counts the time
  away ("away 1:42"), so a break never runs long unnoticed. Pick another
  scene in the widget's "Show time away on". Needs jadeck 0.8.3 to show.

## 0.1.1

- **Scenes:** the scene you tap shows ON AIR right away. Before, the old
  scene could stay red for a second or two, so a tap felt like it needed a
  second one.

## 0.1.0

First release.

- Connects to OBS Studio 28 or later over obs-websocket (password supported); reconnects by itself when OBS restarts. Clear messages for "OBS isn't open", "wrong password" and "can't reach that computer".
- Widgets: Program, Stream, Scenes, Lyrics and Audio.
- OBS works as a jadeck switcher: Cut / Fade, Input bank, Program and the stage screen's "We are live".
- "Make the scenes for me": Starting soon, Pastor, Pastor + lyrics, Slide full screen, Be right back and Ending.
- Read-only connections.
