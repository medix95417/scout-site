package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/roster"
)

// /my-family is the household's contact page, and after this change it
// is an adult's page: every member of the family is on it, all of it is
// editable, and the share toggles set here are the ones that count.

func myFamilyFixture() any {
	return struct {
		baseData
		Members []myFamilyMember
	}{
		baseData: testBase("My Family"),
		Members: []myFamilyMember{
			{
				MemberDetail: roster.MemberDetail{
					ID: "m-adult", FirstName: "Jo", LastName: "Brennan", MemberType: "adult",
					Email: "jo@example.com", CellPhone: "555-0100", ReleaseEmail: true,
					FamilyAddress: "1 Oak St", ReleaseAddress: true,
				},
				RoleLabels: []string{"Parent"},
			},
			{
				MemberDetail: roster.MemberDetail{
					ID: "m-scout", FirstName: "Alex", LastName: "Brennan", MemberType: "youth",
					Email: "alex@example.com", HomePhone: "555-0101",
				},
				SubGroupName: "Bear Den",
				RoleLabels:   []string{"Scout"},
			},
		},
	}
}

func TestMyFamilyShowsEveryMemberAsEditable(t *testing.T) {
	out := renderPage(t, "my-family.html", myFamilyFixture())

	for _, id := range []string{"m-adult", "m-scout"} {
		if !strings.Contains(out, `action="/my-family/members/`+id+`"`) {
			t.Errorf("no editable card for %s — an adult manages the whole household, Scouts included", id)
		}
	}
	for _, value := range []string{"jo@example.com", "alex@example.com", "555-0100", "555-0101", "1 Oak St"} {
		if !strings.Contains(out, value) {
			t.Errorf("%q isn't shown", value)
		}
	}
	// Every contact field is a real input, not read-only text.
	for _, field := range []string{`name="email"`, `name="home_phone"`, `name="cell_phone"`, `name="address"`} {
		if !strings.Contains(out, field) {
			t.Errorf("%s is missing from the page", field)
		}
	}
	// And the share toggles, per member plus the shared address.
	for _, toggle := range []string{`name="release_email"`, `name="release_phone"`, `name="release_address"`} {
		if !strings.Contains(out, toggle) {
			t.Errorf("%s is missing — an adult has to be able to set what's shared", toggle)
		}
	}
	// The existing choices come back checked, not silently reset.
	if !strings.Contains(out, `name="release_email" value="1" checked`) {
		t.Error("an already-shared email doesn't render as checked, so saving the form would un-share it")
	}
}

// The page manages contact details. Logins and passwords are a different
// thing in a different place, and a page that mixed them would be a page
// where an adult could take over a Scout's login.
func TestMyFamilyOffersNoPasswordControls(t *testing.T) {
	out := renderPage(t, "my-family.html", myFamilyFixture())

	if strings.Contains(out, `type="password"`) {
		t.Error("the page has a password field")
	}
	for _, needle := range []string{"/reset-password", "new_password", "Reset password"} {
		if strings.Contains(out, needle) {
			t.Errorf("the page offers %q; passwords are not managed here", needle)
		}
	}
}

// An adult setting a toggle here overrides what that person set for
// themselves, and the page has to say so — otherwise the first surprise
// is a phone number reappearing in the directory.
func TestMyFamilyExplainsThatItOverrides(t *testing.T) {
	out := renderPage(t, "my-family.html", myFamilyFixture())

	if !strings.Contains(out, "replaces anything a family member set for themselves") {
		t.Error("the page doesn't explain that what an adult sets here wins")
	}
}

// All three routes on this page ask the same question in the same place.
// Two of them are POSTs, which is where a missed check actually costs
// something.
func TestEveryMyFamilyRouteChecksForAnAdult(t *testing.T) {
	src, err := readSource("my_family.go")
	if err != nil {
		t.Fatalf("reading my_family.go: %v", err)
	}
	for _, fn := range []string{"MyFamily", "MyFamilyUpdateMember", "MyFamilyUpdateAddress"} {
		body, ok := functionBody(src, fn)
		if !ok {
			t.Fatalf("%s is gone from my_family.go; if it moved, move this guard with it", fn)
		}
		if !strings.Contains(body, "requireFamilyAdult") {
			t.Errorf("%s doesn't go through requireFamilyAdult, so a Scout's own login could use it", fn)
		}
	}

	// Being an adult is permission over your own household, not over
	// anyone's member id that happens to be posted.
	body, _ := functionBody(src, "MyFamilyUpdateMember")
	if !strings.Contains(body, "MemberBelongsToFamily") {
		t.Error("MyFamilyUpdateMember no longer checks the member belongs to this family")
	}
}

func TestScoutLoginIsToldWhereToGo(t *testing.T) {
	if !strings.Contains(notAnAdultMsg, "parent or guardian") {
		t.Error("the refusal should say who to ask")
	}
	if !strings.Contains(notAnAdultMsg, "Security") {
		t.Error("the refusal should point out that their own login is still theirs to manage")
	}
}
