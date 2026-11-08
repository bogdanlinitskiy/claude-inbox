# Demo recording

`demo.gif` is the recording at the top of the main README. If you replace it, keep it small: GitHub paints the
README slowly above a few MB, and a wide GIF is unreadable on a phone.

What it shows, in about 20 seconds:

1. The list with one session in **Needs you**.
2. Click it, read the question, answer with an option button.
3. The row moves to **Working**.
4. The Terminal tab, scrolling.
5. The Diff tab.

Target: around 1280px wide, under ~5MB, no audio, no cursor trail.

## Recording it

Record video, then convert — a GIF recorder makes a much bigger file than `ffmpeg` plus `gifski` does.

```bash
# Wayland
wf-recorder -g "$(slurp)" -f demo.mp4

# X11 (1280x800 at offset 0,0)
ffmpeg -f x11grab -framerate 30 -video_size 1280x800 -i :0.0+0,0 demo.mp4

# macOS: Cmd+Shift+5, or
ffmpeg -f avfoundation -framerate 30 -i 1 demo.mp4
```

Then convert, frames through gifski:

```bash
mkdir -p frames && ffmpeg -i demo.mp4 -vf "fps=15,scale=1280:-1:flags=lanczos" frames/%04d.png
gifski --fps 15 --quality 80 -o docs/media/demo.gif frames/*.png
```

Without gifski, ffmpeg alone does a decent job with a generated palette:

```bash
ffmpeg -i demo.mp4 -vf "fps=15,scale=1280:-1:flags=lanczos,split[a][b];[a]palettegen[p];[b][p]paletteuse" docs/media/demo.gif
```

Keep `demo.mp4` out of git; only the GIF is committed.
