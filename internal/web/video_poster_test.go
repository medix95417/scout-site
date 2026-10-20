package web

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/units"
)

// A video's poster, end to end against a real database and a bucket: who may
// make one, who may see one, what ends up stored, and that it goes when the
// video does. The routes are the real handlers behind the real session and
// unit middleware; only the bucket is a stand-in (see memBucket).

type posterFixture struct {
	folderFixture
	bucket  *memBucket
	host    string
	handler http.Handler
	tokens  map[string]string

	publicVideo, privateVideo, photo files.File
}

func newPosterFixture(t *testing.T, name string) posterFixture {
	t.Helper()
	ctx := context.Background()
	f := newFolderFixture(t, fmt.Sprintf("%s-%d", name, time.Now().UnixNano()))
	bucket, store := newMemBucket(t)
	f.h.Storage = store
	p := f.h.Pool

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var familyID, leaderID, scoutID string
	must(p.QueryRow(ctx, `INSERT INTO families(name) VALUES ('Poster test') RETURNING id`).Scan(&familyID))
	t.Cleanup(func() { _, _ = p.Exec(ctx, `DELETE FROM families WHERE id=$1`, familyID) })
	must(p.QueryRow(ctx, `INSERT INTO members(family_id,first_name,last_name,member_type) VALUES($1,'Lead','Er','adult') RETURNING id`, familyID).Scan(&leaderID))
	must(p.QueryRow(ctx, `INSERT INTO members(family_id,first_name,last_name,member_type) VALUES($1,'Sc','Out','youth') RETURNING id`, familyID).Scan(&scoutID))
	_, err := p.Exec(ctx, `INSERT INTO role_assignments(member_id,unit_id,role) VALUES($1,$3,'super_admin'),($2,$3,'scout')`, leaderID, scoutID, f.unitID)
	must(err)

	tokens := map[string]string{}
	for who, member := range map[string]string{"leader": leaderID, "scout": scoutID} {
		var userID string
		must(p.QueryRow(ctx, `INSERT INTO users(family_id,member_id,email,password_hash) VALUES($1,$2,$3,'unused') RETURNING id`,
			familyID, member, fmt.Sprintf("%s-%s@test.invalid", who, f.unitID)).Scan(&userID))
		token, _, err := auth.CreateSession(ctx, p, userID)
		must(err)
		tokens[who] = token
	}

	var host string
	must(p.QueryRow(ctx, `SELECT hostname FROM units WHERE id=$1`, f.unitID).Scan(&host))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /files/{id}/poster", f.h.FilePoster)
	// The CSRF middleware reads the multipart body before a handler runs; this
	// stands in for it, since what is under test is the handler.
	mux.HandleFunc("POST /files/{id}/poster", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(32 << 20)
		f.h.FilePosterUpload(w, r)
	})
	mux.HandleFunc("POST /files/{id}/delete", f.h.FileDelete)

	fx := posterFixture{
		folderFixture: f,
		bucket:        bucket,
		host:          host,
		handler:       units.Middleware(p)(auth.WithUser(p)(mux)),
		tokens:        tokens,
	}
	mk := func(name, contentType string, public bool) files.File {
		created, err := files.Create(ctx, p, files.File{
			UnitID: f.unitID, Filename: name, ContentType: contentType, SizeBytes: 10,
			StorageKey: f.unitID + "/documents/" + name, Category: files.CategoryGeneral, Public: public,
		})
		must(err)
		return created
	}
	fx.publicVideo = mk("camp.mp4", "video/mp4", true)
	fx.privateVideo = mk("members.mp4", "video/mp4", false)
	fx.photo = mk("photo.jpg", "image/jpeg", true)
	return fx
}

func (f posterFixture) do(method, path, who string, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
	if body == nil {
		body = &bytes.Buffer{}
	}
	r := httptest.NewRequest(method, "http://"+f.host+path, body)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if tok, ok := f.tokens[who]; ok {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func (f posterFixture) postPoster(fileID, who string, picture []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if picture != nil {
		part, _ := mw.CreateFormFile("poster", "poster.jpg")
		part.Write(picture)
	}
	mw.Close()
	return f.do("POST", "/files/"+fileID+"/poster", who, &body, mw.FormDataContentType())
}

func (f posterFixture) getPoster(fileID, who string) *httptest.ResponseRecorder {
	return f.do("GET", "/files/"+fileID+"/poster", who, nil, "")
}

func frameJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 120, 255})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	return buf.Bytes()
}

func framePNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{200, uint8(x), uint8(y), 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func TestALeaderCanGiveAVideoAPosterAndAnyoneCanSeeIt(t *testing.T) {
	f := newPosterFixture(t, "poster-ok")
	key := files.PosterKey(f.unitID, f.publicVideo.ID)

	if got := f.getPoster(f.publicVideo.ID, ""); got.Code != http.StatusNotFound {
		t.Fatalf("before any poster: status = %d, want 404", got.Code)
	}

	if w := f.postPoster(f.publicVideo.ID, "leader", frameJPEG(320, 180)); w.Code != http.StatusNoContent {
		t.Fatalf("leader's upload: status = %d (%s), want 204", w.Code, strings.TrimSpace(w.Body.String()))
	}
	if !f.bucket.has(key) {
		t.Fatalf("nothing stored at %s; bucket holds %v", key, f.bucket.keys())
	}
	if has, _ := files.HasPoster(context.Background(), f.h.Pool, f.publicVideo.ID, f.unitID); !has {
		t.Error("the row doesn't record that the video has a poster")
	}

	// A signed-out visitor sees it, because the video is public.
	w := f.getPoster(f.publicVideo.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET poster: status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", got)
	}
	for header, want := range map[string]string{
		"Content-Security-Policy": "sandbox",
		"X-Content-Type-Options":  "nosniff",
		"Cache-Control":           "private, no-store",
	} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if _, err := jpeg.Decode(w.Body); err != nil {
		t.Errorf("what was served is not a JPEG: %v", err)
	}
}

// Whatever the browser says it is, what is stored — and then served from this
// origin — is a JPEG this server wrote, at the size a poster is shown.
func TestThePosterStoredIsAlwaysASmallJPEG(t *testing.T) {
	f := newPosterFixture(t, "poster-norm")

	cases := []struct {
		name string
		in   []byte
		maxW int
	}{
		{"a big JPEG is shrunk", frameJPEG(2000, 1500), 640},
		{"a PNG becomes a JPEG", framePNG(300, 200), 300},
	}
	for _, c := range cases {
		video := f.publicVideo
		if w := f.postPoster(video.ID, "leader", c.in); w.Code != http.StatusNoContent {
			t.Fatalf("%s: status = %d (%s)", c.name, w.Code, strings.TrimSpace(w.Body.String()))
		}
		stored := f.bucket.get(files.PosterKey(f.unitID, video.ID))
		if len(stored) < 2 || stored[0] != 0xFF || stored[1] != 0xD8 {
			t.Errorf("%s: what was stored is not a JPEG", c.name)
			continue
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(stored))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if longest := max(cfg.Width, cfg.Height); longest > 640 {
			t.Errorf("%s: stored at %dx%d, longer than a poster needs", c.name, cfg.Width, cfg.Height)
		}
		if cfg.Width > c.maxW {
			t.Errorf("%s: stored %d wide, want at most %d", c.name, cfg.Width, c.maxW)
		}
	}
}

func TestOnlyAVideoCanHaveAPoster(t *testing.T) {
	f := newPosterFixture(t, "poster-kind")
	w := f.postPoster(f.photo.ID, "leader", frameJPEG(64, 36))
	if w.Code != http.StatusBadRequest {
		t.Errorf("a poster for a photo: status = %d, want 400", w.Code)
	}
	if k := f.bucket.keys(); len(k) != 0 {
		t.Errorf("something was stored for a photo: %v", k)
	}
}

func TestPosterUploadRefusals(t *testing.T) {
	f := newPosterFixture(t, "poster-refuse")
	other := newPosterFixture(t, "poster-other")
	video := f.publicVideo.ID

	oversize := bytes.Repeat([]byte{0xFF}, maxPosterUploadBytes+1)
	cases := []struct {
		name string
		do   func() *httptest.ResponseRecorder
		want int
	}{
		{"signed out", func() *httptest.ResponseRecorder { return f.postPoster(video, "", frameJPEG(64, 36)) }, http.StatusUnauthorized},
		{"a Scout, who cannot edit the site's content", func() *httptest.ResponseRecorder { return f.postPoster(video, "scout", frameJPEG(64, 36)) }, http.StatusForbidden},
		{"bytes that are not a picture", func() *httptest.ResponseRecorder {
			return f.postPoster(video, "leader", []byte("<html>not a picture</html>"))
		}, http.StatusBadRequest},
		{"no picture in the request", func() *httptest.ResponseRecorder { return f.postPoster(video, "leader", nil) }, http.StatusBadRequest},
		{"a picture over the limit", func() *httptest.ResponseRecorder { return f.postPoster(video, "leader", oversize) }, http.StatusRequestEntityTooLarge},
		{"a file id that does not exist", func() *httptest.ResponseRecorder {
			return f.postPoster("00000000-0000-0000-0000-000000000000", "leader", frameJPEG(64, 36))
		}, http.StatusNotFound},
		// The other unit's leader holds a perfectly good session — for the other
		// unit. This unit's file is simply not there for them.
		{"a leader of the other unit", func() *httptest.ResponseRecorder { return other.postPoster(video, "leader", frameJPEG(64, 36)) }, http.StatusNotFound},
	}
	for _, c := range cases {
		if got := c.do(); got.Code != c.want {
			t.Errorf("%s: status = %d (%s), want %d", c.name, got.Code, strings.TrimSpace(got.Body.String()), c.want)
		}
	}
	for _, fx := range []posterFixture{f, other} {
		if k := fx.bucket.keys(); len(k) != 0 {
			t.Errorf("a refused request still stored something: %v", k)
		}
	}
	if has, _ := files.HasPoster(context.Background(), f.h.Pool, video, f.unitID); has {
		t.Error("a refused request marked the video as having a poster")
	}
}

// A frame of a members-only video is the video's content, so its poster is
// shown to exactly the people the video is.
func TestAMembersOnlyVideosPosterFollowsTheVideosRule(t *testing.T) {
	f := newPosterFixture(t, "poster-private")
	if w := f.postPoster(f.privateVideo.ID, "leader", frameJPEG(64, 36)); w.Code != http.StatusNoContent {
		t.Fatalf("upload: status = %d", w.Code)
	}

	out := f.getPoster(f.privateVideo.ID, "")
	if out.Code != http.StatusSeeOther || !strings.HasPrefix(out.Header().Get("Location"), "/login") {
		t.Errorf("signed out: status = %d, Location = %q, want a redirect to /login", out.Code, out.Header().Get("Location"))
	}
	if in := f.getPoster(f.privateVideo.ID, "scout"); in.Code != http.StatusOK {
		t.Errorf("a signed-in member of the unit: status = %d, want 200", in.Code)
	}
}

func TestDeletingAVideoDeletesItsPoster(t *testing.T) {
	f := newPosterFixture(t, "poster-delete")
	key := files.PosterKey(f.unitID, f.publicVideo.ID)
	if w := f.postPoster(f.publicVideo.ID, "leader", frameJPEG(64, 36)); w.Code != http.StatusNoContent {
		t.Fatalf("upload: status = %d", w.Code)
	}
	if !f.bucket.has(key) {
		t.Fatal("poster not stored")
	}

	if w := f.do("POST", "/files/"+f.publicVideo.ID+"/delete", "leader", nil, ""); w.Code != http.StatusSeeOther {
		t.Fatalf("delete: status = %d, want 303", w.Code)
	}
	if f.bucket.has(key) {
		t.Error("the poster was left behind in the bucket")
	}
}

// --- the address, and what a page does with it ---------------------------

func TestPosterURL(t *testing.T) {
	cases := map[string]string{
		"/files/abc-123/download":       "/files/abc-123/poster",
		"https://example.org/video.mp4": "", // somebody else's video: no poster of ours
		"/files/abc/thumb":              "",
		"/files//download":              "",
		"/files/a/b/download":           "",
		"":                              "",
		"/news/abc/download":            "",
	}
	for in, want := range cases {
		if got := posterURL(in); got != want {
			t.Errorf("posterURL(%q) = %q, want %q", in, got, want)
		}
	}
}
