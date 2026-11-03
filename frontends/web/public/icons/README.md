# Home Screen icons

Source: `../favicon.svg`, the existing Reliant blue/purple brand mark. No redrawing or rounded-corner mask is applied; the OS supplies its own mask. White is the opaque launch/icon background, matching `manifest.webmanifest`.

Reproduce from `frontends/web` with ImageMagick (a development tool, not an app dependency):

```sh
magick -background none public/favicon.svg -resize 384x384 -gravity center -background white -extent 512x512 public/icons/reliant-512.png
magick public/icons/reliant-512.png -resize 192x192 public/icons/reliant-192.png
magick -background none public/favicon.svg -resize 320x320 -gravity center -background white -extent 512x512 public/icons/reliant-maskable-512.png
magick public/icons/reliant-512.png -resize 180x180 public/apple-touch-icon.png
```

The ordinary icons have 12.5% horizontal padding. The maskable mark is narrower (62.5% of the canvas); its approximately 320×184 visible bounds fit entirely inside the central safe circle of radius 204.8px (40% of 512). Its background fills the whole square. `purpose: maskable` is separate from `purpose: any` so browsers do not use a tightly packed favicon as a masked icon.

The manifest intentionally uses origin-relative URLs and starts at `/m/chats`. This is Home Screen launch support, not offline support; no service worker is registered.
