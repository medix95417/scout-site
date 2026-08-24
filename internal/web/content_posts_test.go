package web

import (
	"reflect"
	"testing"

	"github.com/47-yonkers/scout-site/internal/content"
)

func TestGalleryFileIDs(t *testing.T) {
	photos := []content.GalleryPhoto{
		{URL: "/files/abc-123/download"},
		{URL: "https://example.com/photo.jpg"},
		{URL: "/files/def-456/download"},
	}
	got := galleryFileIDs(photos)
	want := []string{"abc-123", "def-456"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestKeepViewableGalleryPhotos(t *testing.T) {
	external := content.GalleryPhoto{URL: "https://example.com/photo.jpg", Caption: "external"}
	publicOwn := content.GalleryPhoto{URL: "/files/public-1/download", Caption: "public"}
	privateOwn := content.GalleryPhoto{URL: "/files/private-1/download", Caption: "private"}
	photos := []content.GalleryPhoto{external, publicOwn, privateOwn}

	t.Run("keeps external links and public own-file links, drops private", func(t *testing.T) {
		publicIDs := map[string]bool{"public-1": true}
		got := keepViewableGalleryPhotos(photos, publicIDs)
		want := []content.GalleryPhoto{external, publicOwn}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("empty public set drops every own-file link, keeps external", func(t *testing.T) {
		got := keepViewableGalleryPhotos(photos, map[string]bool{})
		want := []content.GalleryPhoto{external}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
}
