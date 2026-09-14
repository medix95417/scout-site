# Unit numeral images

Drop a JPEG or PNG per digit in here, named `bsa-0.jpg` … `bsa-9.jpg`
(`.png` works too), and a pack's homepage hero will draw its unit number
with them instead of the CSS numeral it falls back to.

The BSA numeral clipart at
`clipart.usscouts.org/library/BSA_Insignia/Troop_numbers/` is where these
usually come from. **Download them and commit them here — do not hotlink
that site.** Three reasons, in order of how quickly they bite:

1. Those URLs are `http://`. This site is served over `https://`, so a
   browser treats them as mixed content and refuses to load them. The
   numerals would not appear at all.
2. `img-src` in the Content-Security-Policy (see internal/csp) allows
   `https:` and nothing else, so the policy refuses them too.
3. Hotlinking points every visitor to this site's front page at somebody
   else's server. It can stop working any day, and it tells that server
   who is reading this site.

Only a complete set is used: if any digit of the unit's number has no
image here, the whole numeral falls back to the CSS one, so a unit never
ends up with half its number as pictures and half as text. See
numeralImagesFor in internal/web/unit_wordmark.go.
