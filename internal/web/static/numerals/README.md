# Unit numeral images

A unit's homepage hero draws its unit number with the program's own
numeral patches when the images for it are here, and falls back to a
plain (pack: boxed red-on-white) numeral when they aren't.

## Layout

One directory per program, one image per digit inside it:

```
numerals/troop/0.jpg … 9.jpg   Scouts BSA numerals (green on tan)
numerals/pack/0.jpg  … 9.jpg   Cub Scout numerals (red on white)
```

`.jpg`, `.png`, `.jpeg`, `.gif` and `.svg` are all accepted, so
`numerals/pack/4.png` works as well as `numerals/pack/4.jpg`.

The split by program is the point: the two look nothing alike, and a
Scouts BSA numeral on a Cub Scout pack's homepage is simply the wrong
insignia. One program's set is never substituted for the other's.

Only a complete set is used: if any digit of the unit's number has no
image in that program's directory, the whole numeral falls back, so a
unit never ends up with half its number as pictures and half as text.
See `numeralImagesFor` in `internal/web/unit_wordmark.go`.

## Adding a set

Commit the images here — **do not hotlink** the clipart sites these
usually come from (e.g.
`clipart.usscouts.org/library/BSA_Insignia/Troop_numbers/`). Three
reasons, in order of how quickly they bite:

1. Those URLs are `http://`. This site is served over `https://`, so a
   browser treats them as mixed content and refuses to load them. The
   numerals would not appear at all.
2. `img-src` in the Content-Security-Policy (see `internal/csp`) allows
   `https:` and nothing else, so the policy refuses them too.
3. Hotlinking points every visitor to this site's front page at somebody
   else's server. It can stop working any day, and it tells that server
   who is reading this site.

These are rendered at roughly one line-height tall, so they want to be
small: trim the surrounding white, resize to about 240px tall, and save
as progressive JPEG at quality 85 — that lands around 16KB per digit,
against ~100KB for the same image as a PNG. They are embedded in the
binary (`//go:embed all:static`), so every digit committed here is
carried by every deploy.
