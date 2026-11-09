# FolderBridge tray artwork

`folder-link-source.png` is the original ChatGPT Images PNG supplied by the user
for FolderBridge: two teal/blue folders connected by a bridge. The separate
root-level upload was left unchanged. This source image is retained for editing
but is **not** embedded in the executable.

The three multi-frame ICOs are embedded by `tray_icons_windows.go`:

- `tray-normal.ico`: supplied folder-link design, no status badge.
- `tray-paused.ico`: the same design with an amber pause badge.
- `tray-error.ico`: the same design with a red exclamation badge.

All contain 16, 20, 24, 32, 48 and 64 pixel square frames, with 32-bit BGRA DIBs,
alpha transparency and ICO masks. These are runtime tray icons, not Explorer/EXE
resources. Ordinary `go build .` embeds them without an image library, resource
compiler, extra runtime or external files.

## Asset preparation

Preserve the supplied colors, bridge shape and wide aspect ratio; do not stretch
or redraw the design. Remove near-invisible generation speckles by zeroing alpha
below 16/255, crop to the remaining alpha bounds, then center the artwork within
each square with at least one output pixel of padding. Prepare at four times the
output resolution and downsample with Lanczos antialiasing.

Badge circles occupy approximately 46% of the square width at the bottom right,
with a white outline. Pause is amber `#ffcb45` with dark `#273044` bars; error is
red `#dc353f` with a white exclamation mark. Do not recolor the original folders.

The local preparation script used Pillow to export the checked-in ICO assets;
Pillow is **not a build or runtime dependency**. Native Windows tests validate
all embedded frames, selected sizes, HICON dimensions, state selection/reuse and
actual handle destruction. Interactive taskbar appearance still needs manual
acceptance; the light/dark enlarged-pixel preview is a local artifact at
`dist/tray-icon-preview.png`. Evidence: `review/TRAY-ICON.md`.
