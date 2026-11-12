# Cody app icon

Generated with the built-in image generation tool from the GUI's mint robot buddy.
Transparent padding is preserved. No runtime dependency is required.

- `cody.png`: original 1254 × 1254 RGBA artwork; usable for Linux launchers.
- `cody.icns`: macOS app icon (16 through 1024 pixels), bundled by `../macos/build.sh`.
- `cody.ico`: Windows icon (16, 24, 32, 48, 64, 128, 256 pixels).

Windows/Linux assets are provided for packaging; Cody currently has a native
macOS shell only. A CLI running in a terminal uses the terminal's taskbar icon.
The ICNS/ICO files are standard format conversions of the PNG using macOS `sips`
and `iconutil` (ICO stores the resized PNG frames).

## Generation prompt

Use case: logo-brand. Create one finished 1024x1024 desktop application icon for Cody, the coding assistant. Reference image role: the attached GUI screenshot is only a visual identity reference; use the tiny mint robot buddy immediately left of 'cody' in the upper-left sidebar. Do not reproduce the GUI or its text. Preserve that friendly robot's recognizable simple geometry: rounded rectangular face/body outline, two round eyes, short horizontal rounded mouth, small rounded tab centered on top, two little feet. Primary request: a minimalist sleek premium app icon, centered mint/sage robot buddy on an almost-black charcoal softly rounded square tile, generous transparent padding outside the tile. Bold clean shapes readable at 16px, slightly rounded corners throughout, quiet flat treatment with at most very subtle depth. Match the screenshot's restrained mint and charcoal palette. Robot occupies about 62% of canvas width, tile about 86%. Orthographic straight-on, symmetrical. No text, no letters, no code brackets, no extra objects, no gradients/glow, no mockup, no drop shadow outside tile, no watermark. Transparent pixels outside the rounded tile; deliver only the icon artwork.
