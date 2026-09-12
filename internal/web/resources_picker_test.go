package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/resources"
)

// A resource is a handbook, a form, a map, or a link to one somewhere
// else. It is not a photo — those belong on Photos — and a picker that
// offered every photo in the library alongside the documents was both
// long and wrong.

func resourcesPage(groups []files.EventFileGroup, ungrouped []files.File) any {
	return struct {
		baseData
		Resources          []resourceRow
		CanManage          bool
		DocumentGroups     []files.EventFileGroup
		DocumentsUngrouped []files.File
		HasDocuments       bool
	}{
		baseData:           testBase("Resources"),
		Resources:          []resourceRow{{Resource: resources.Resource{ID: "r1", Title: "Handbook"}}},
		CanManage:          true,
		DocumentGroups:     groups,
		DocumentsUngrouped: ungrouped,
		HasDocuments:       len(groups) > 0 || len(ungrouped) > 0,
	}
}

func documentFixtures() ([]files.EventFileGroup, []files.File) {
	groups := []files.EventFileGroup{{
		EventID: "e-camp", EventTitle: "Summer Camp",
		Files: []files.File{
			{ID: "d1", Filename: "packing-list.pdf", DisplayName: "Packing list", ContentType: "application/pdf"},
			{ID: "d2", Filename: "map.pdf", ContentType: "application/pdf"},
		},
	}}
	ungrouped := []files.File{
		{ID: "d3", Filename: "handbook.pdf", ContentType: "application/pdf"},
	}
	return groups, ungrouped
}

func TestResourcePickerGroupsDocumentsByEvent(t *testing.T) {
	groups, ungrouped := documentFixtures()
	out := renderPage(t, "resources.html", resourcesPage(groups, ungrouped))

	if !strings.Contains(out, "Summer Camp") {
		t.Error("the picker doesn't group by event")
	}
	if !strings.Contains(out, "Not linked to an event") {
		t.Error("documents with no event have nowhere to go")
	}
	for _, id := range []string{"d1", "d2", "d3"} {
		if !strings.Contains(out, `<input type="radio" name="file_id" value="`+id+`"`) {
			t.Errorf("document %s is not offered", id)
		}
	}
	// One resource points at one document, so the rows are radios and
	// nothing is pre-selected — picking has to be deliberate.
	if strings.Contains(out, `name="file_id" value="d1" checked`) {
		t.Error("a document is pre-selected; the old flat dropdown's first option was the same accident")
	}
	// The old control was a <select> of every file in the library.
	if strings.Contains(out, `<select class="w-full rounded-md border border-gray-300 px-3 py-2 text-sm" name="file_id">`) {
		t.Error("the flat every-file dropdown is still rendered")
	}
}

func TestResourcePickerSaysWhenThereAreNoDocuments(t *testing.T) {
	out := renderPage(t, "resources.html", resourcesPage(nil, nil))

	if !strings.Contains(out, "No documents in your") {
		t.Error("with an empty library the picker should say so and point at /files")
	}
	if strings.Contains(out, `name="file_id"`) {
		t.Error("an empty picker still renders inputs")
	}
	// The other half of the form — a plain link — still has to work.
	if !strings.Contains(out, `data-resource-panel="link"`) {
		t.Error("the direct-link option went missing")
	}
}

func TestIsDocumentFile(t *testing.T) {
	cases := map[string]bool{
		"application/pdf":    true,
		"text/plain":         true,
		"application/msword": true,
		"":                   true, // unknown type: treated as a document, since it isn't known to be a picture
		"image/jpeg":         false,
		"image/png":          false,
		"video/mp4":          false,
		"video/quicktime":    false,
	}
	for contentType, want := range cases {
		if got := isDocumentFile(contentType); got != want {
			t.Errorf("isDocumentFile(%q) = %v, want %v", contentType, got, want)
		}
	}
}
