package web

// The storage half of moving an embedded image out of an email body.
//
// internal/newsletter/inline_images.go explains why this is worth doing
// at all (Gmail and Outlook will not render a data: URI image, and the
// body is carried once per recipient). This file is what actually holds
// the bytes: it puts them in the unit's file library, marks the file
// public, and hands back the URL to point the email at.
//
// Three things about it are deliberate.
//
// PUBLIC. A file used in an email has to be fetchable with no login,
// because the person opening the email does not have one — their mail
// client fetches the image as an anonymous stranger. So these rows are
// created with is_public set, the same thing a leader does by hand today
// when they pick a library photo for the public homepage. The URL still
// contains an unguessable id and nothing links to it, but it is
// genuinely public, and that is worth saying plainly rather than
// burying: an image you would not put on the public site does not belong
// in an email either, since every recipient's mail provider fetches it
// too.
//
// NEVER FATAL. Every failure path returns "", which
// newsletter.ImageStore defines as "leave this one embedded". A leader
// mid-save must not lose a draft because storage was briefly
// unreachable, and the email still sends either way.
//
// CONTENT-ADDRESSED, PER UNIT. The storage key is the SHA-256 of the
// bytes under the unit's own prefix, so the same logo saved five times
// while editing, or reused in next month's newsletter, resolves to the
// one file already stored — while the Troop and the Pack still keep
// separate copies, because a shared one would be a file owned by
// whichever unit happened to save first.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strings"

	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/newsletter"
	"github.com/47-yonkers/scout-site/internal/thumbnail"
)

// maxInlineImageBytes caps one decoded embedded image. Well under
// maxUploadFileSize, because this is not a leader deliberately uploading
// a file — it is whatever a template happened to carry, decoded from a
// body that itself has to stay under settings.MaxEmailTemplateBytes.
// Anything larger stays embedded, and is somebody's mistake to notice.
const maxInlineImageBytes = 10 << 20 // 10 MB

// maxInlineImagesPerBody bounds how many images one save will host, so a
// strange or hostile body cannot turn a single form post into hundreds
// of storage writes. The rest stay embedded.
const maxInlineImagesPerBody = 40

// inlineImageDisplayName labels these in the file library so a leader
// browsing it can tell where they came from — otherwise they show up as
// anonymous hash-named files nobody dares delete.
const inlineImageDisplayName = "Email image"

// hostInlineImages sanitizes an email body and, where the site has
// storage configured, rewrites images embedded in it to point at hosted
// copies instead.
//
// A drop-in replacement for newsletter.Sanitize at any save path that
// stores an email body, and identical to it when storage is
// unconfigured — the optional-integration rule (see CLAUDE.md): no
// storage means no hosting, not a broken composer.
func (h *Handlers) hostInlineImages(ctx context.Context, unitID, siteURL string, actorID *string, body string) string {
	if h.Storage == nil {
		return newsletter.Sanitize(body)
	}
	host := &inlineImageHost{
		unitID:  unitID,
		siteURL: siteURL,
		actorID: actorID,
		lookup: func(ctx context.Context, unitID, key string) (files.File, bool, error) {
			return files.ByStorageKey(ctx, h.Pool, unitID, key)
		},
		put: func(ctx context.Context, key string, data []byte, contentType string) error {
			return h.Storage.Put(ctx, key, bytes.NewReader(data), int64(len(data)), contentType)
		},
		create: func(ctx context.Context, f files.File) (files.File, error) {
			return files.Create(ctx, h.Pool, f)
		},
		// Eagerly, same as an ordinary upload, and best-effort for the
		// same reason: FileThumbnail regenerates on demand if this fails.
		cacheThumb: func(ctx context.Context, key string, data []byte) {
			thumb, err := thumbnail.Generate(data)
			if err != nil {
				log.Printf("web: generating thumbnail for email image: %v", err)
				return
			}
			if err := h.Storage.Put(ctx, key+thumbStorageSuffix, bytes.NewReader(thumb), int64(len(thumb)), "image/jpeg"); err != nil {
				log.Printf("web: caching thumbnail for email image: %v", err)
			}
		},
	}
	return newsletter.SanitizeHostingImages(body, host.store(ctx))
}

// inlineImageHost holds the state and the dependencies of hosting the
// images from ONE body. Single-use: it counts what it has hosted and
// remembers what it has already seen, both of which are per-save.
//
// The four function fields are the only things here that touch a
// database or a bucket, which is what lets the policy above them — the
// size cap, the count cap, what counts as an image, when a copy is
// reused — be tested for real rather than by inspection.
type inlineImageHost struct {
	unitID  string
	siteURL string
	actorID *string

	lookup     func(ctx context.Context, unitID, key string) (files.File, bool, error)
	put        func(ctx context.Context, key string, data []byte, contentType string) error
	create     func(ctx context.Context, f files.File) (files.File, error)
	cacheThumb func(ctx context.Context, key string, data []byte)

	hosted int
	seen   map[string]string // content hash -> URL, for an image repeated within one body
}

func (i *inlineImageHost) store(ctx context.Context) newsletter.ImageStore {
	return func(data []byte, declaredType string) string {
		return i.hostOne(ctx, data, declaredType)
	}
}

func (i *inlineImageHost) hostOne(ctx context.Context, data []byte, declaredType string) string {
	if len(data) == 0 || len(data) > maxInlineImageBytes {
		return ""
	}
	if i.hosted >= maxInlineImagesPerBody {
		return ""
	}

	digest := hex.EncodeToString(sha256Of(data))
	if url, ok := i.seen[digest]; ok {
		// Counted once, not once per appearance: a spacer image repeated
		// down a template is one stored file, and shouldn't burn the
		// budget that exists to bound storage writes.
		return url
	}

	// The declared type is the document's claim; sniffContentType
	// re-derives it from the bytes (see file_serving.go for why the
	// claim can't be trusted). Anything that doesn't sniff as a format a
	// browser may render in place is not something to store and serve
	// back from our own origin, so it stays embedded — where it is
	// inert, because a data: URI in an <img> is only ever decoded as
	// image bytes.
	contentType := sniffContentType(data, declaredType)
	if !strings.HasPrefix(contentType, "image/") || !inlineRenderableTypes[contentType] {
		return ""
	}

	key := i.unitID + "/email-images/" + digest + extensionForImage(contentType)

	// Already stored — by an earlier save of this same draft, or by
	// another message in this unit using the same image.
	f, found, err := i.lookup(ctx, i.unitID, key)
	if err != nil {
		log.Printf("web: looking up hosted email image: %v", err)
		return ""
	}
	if !found {
		if err := i.put(ctx, key, data, contentType); err != nil {
			log.Printf("web: storing email image: %v", err)
			return ""
		}
		i.cacheThumb(ctx, key, data)

		f, err = i.create(ctx, files.File{
			UnitID:      i.unitID,
			Filename:    "email-image-" + digest[:12] + extensionForImage(contentType),
			DisplayName: inlineImageDisplayName,
			ContentType: contentType,
			SizeBytes:   int64(len(data)),
			StorageKey:  key,
			Category:    files.CategoryGeneral,
			UploadedBy:  i.actorID,
			// See the file header: an email image is fetched by a mail
			// client that has no session.
			Public: true,
		})
		if err != nil {
			// The object is in storage now with no row pointing at it.
			// Harmless (see files.Create on that ordering), and the next
			// save derives the same content-addressed key and overwrites
			// it rather than leaking a second copy.
			log.Printf("web: recording hosted email image: %v", err)
			return ""
		}
	}

	url := i.siteURL + "/files/" + f.ID + "/download"
	if i.seen == nil {
		i.seen = map[string]string{}
	}
	i.seen[digest] = url
	i.hosted++
	return url
}

func sha256Of(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// extensionForImage gives a stored object a recognizable suffix. Purely
// cosmetic — for whoever is looking at the bucket — since the database
// row carries the content type that actually gets served.
func extensionForImage(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/avif":
		return ".avif"
	case "image/tiff":
		return ".tiff"
	default:
		return ""
	}
}
