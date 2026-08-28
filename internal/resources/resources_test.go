package resources

import "testing"

func fileResource(resourcePublic, filePublic bool) Resource {
	id := "some-file-id"
	return Resource{FileID: &id, IsPublic: resourcePublic, FileIsPublic: filePublic}
}

func linkResource(public bool) Resource {
	u := "https://example.org/handbook"
	return Resource{URL: &u, IsPublic: public}
}

// TestPubliclyReachableButPrivate covers the one combination of these two
// flags that surprises a leader.
//
// resources.is_public and files.is_public answer different questions —
// "is this listed and downloadable on the public resources page" versus
// "is this file downloadable at /files/{id}/download" — and they can
// disagree. Only one direction of disagreement matters: a resource marked
// members-only whose document is still public, because that looks
// protected and isn't.
//
// The reverse (public resource, members-only file) is fine: the resources
// download route serves it on the resource's own flag, which is what the
// leader asked for.
func TestPubliclyReachableButPrivate(t *testing.T) {
	cases := []struct {
		name string
		res  Resource
		want bool
	}{
		{
			name: "members-only resource, public file — the leaky case",
			res:  fileResource(false, true),
			want: true,
		},
		{
			name: "members-only resource, members-only file — consistent",
			res:  fileResource(false, false),
			want: false,
		},
		{
			name: "public resource, public file — consistent",
			res:  fileResource(true, true),
			want: false,
		},
		{
			name: "public resource, members-only file — intended, resource flag wins",
			res:  fileResource(true, false),
			want: false,
		},
		{
			name: "members-only link has no file to leak",
			res:  linkResource(false),
			want: false,
		},
		{
			name: "public link has no file to leak",
			res:  linkResource(true),
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.res.PubliclyReachableButPrivate(); got != c.want {
				t.Errorf("PubliclyReachableButPrivate() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestPubliclyReachableButPrivate_IgnoresAZeroValue — a Resource that
// hasn't been loaded from the database (no FileID) must not warn, or every
// link resource would carry a scary notice about a file it doesn't have.
func TestPubliclyReachableButPrivate_IgnoresAZeroValue(t *testing.T) {
	if (Resource{}).PubliclyReachableButPrivate() {
		t.Error("a zero-value Resource should not report a leak")
	}
}
