package newsletter

// StarterTemplate is a pre-written newsletter a leader can start a new
// draft from instead of a blank page — picked client-side on
// /admin/newsletters/new (see admin-newsletter-form.html), so choosing one
// costs no round trip. Body is real HTML, already in the shape the WYSIWYG
// editor understands, and still gets run through Sanitize like anything
// else the editor saves (a leader can edit it before ever saving a draft).
type StarterTemplate struct {
	Name    string
	Subject string
	Body    string
}

// StarterTemplates returns the built-in starting points offered on
// /admin/newsletters/new, unit-type-aware the same way
// internal/content.HomepageSections is — a Pack's "Monthly Update" talks
// about dens and Pinewood Derby, a Troop's about patrols and campouts.
func StarterTemplates(unitType string) []StarterTemplate {
	if unitType == "troop" {
		return []StarterTemplate{
			{
				Name:    "Monthly Update",
				Subject: "Troop 47 Monthly Update",
				Body: `<h2>This Month in Troop 47</h2>
<p>Hi families,</p>
<p>Here's what's coming up this month:</p>
<ul>
<li>Weekly meeting time/location updates</li>
<li>Upcoming campout details</li>
<li>Advancement and merit badge opportunities</li>
</ul>
<p>See you at the next meeting!</p>`,
			},
			{
				Name:    "Event Announcement",
				Subject: "Upcoming Event — Troop 47",
				Body: `<h2>You're Invited!</h2>
<p>We have an upcoming event we'd love for your Scout to join:</p>
<p><strong>What:</strong> [event name]<br>
<strong>When:</strong> [date/time]<br>
<strong>Where:</strong> [location]</p>
<p>Please RSVP on the <a href="/calendar">calendar</a> so we can plan accordingly.</p>`,
			},
		}
	}
	return []StarterTemplate{
		{
			Name:    "Monthly Update",
			Subject: "Pack 47 Monthly Update",
			Body: `<h2>This Month in Pack 47</h2>
<p>Hi families,</p>
<p>Here's what's coming up this month:</p>
<ul>
<li>Pack meeting time/location updates</li>
<li>Den meeting schedules</li>
<li>Pinewood Derby and other upcoming events</li>
</ul>
<p>See you at the next meeting!</p>`,
		},
		{
			Name:    "Event Announcement",
			Subject: "Upcoming Event — Pack 47",
			Body: `<h2>You're Invited!</h2>
<p>We have an upcoming event we'd love for your Scout to join:</p>
<p><strong>What:</strong> [event name]<br>
<strong>When:</strong> [date/time]<br>
<strong>Where:</strong> [location]</p>
<p>Please RSVP on the <a href="/calendar">calendar</a> so we can plan accordingly.</p>`,
		},
	}
}

// ProspectStarterTemplates are the starting points offered on the
// prospect-campaign composer.
//
// They live here rather than in internal/prospect because they are the
// same kind of thing as StarterTemplates above — pre-written HTML bodies
// for the same editor — and splitting them across two packages would mean
// two places to look when the wording needs a change.
//
// The unit's own name is substituted in rather than left as a
// placeholder: these go to people who have never met the unit, and a
// letter that opens "[unit name]" because somebody forgot to fill it in
// is worse than no letter.
func ProspectStarterTemplates(unitName, unitType string) []StarterTemplate {
	program := "Cub Scouts"
	youth := "your child"
	meeting := "den meeting"
	if unitType == "troop" {
		program = "Scouts BSA"
		youth = "your Scout"
		meeting = "troop meeting"
	}

	return []StarterTemplate{
		{
			Name:    "Thanks for your interest",
			Subject: "Thanks for your interest in " + unitName,
			Body: `<h2>Thanks for reaching out!</h2>
<p>Hi there,</p>
<p>Thank you for your interest in ` + unitName + `. We'd love to have ` + youth + ` visit us and see what ` + program + ` is all about — there's no commitment, and the first visit is always free.</p>
<p><strong>When we meet:</strong> [day and time]<br>
<strong>Where:</strong> [location]</p>
<p>Just turn up, or reply to this email and we'll look out for you.</p>
<p>Yours in Scouting,<br>
` + unitName + `</p>`,
		},
		{
			Name:    "Invitation to an open house",
			Subject: "Come and visit " + unitName,
			Body: `<h2>Come and see us</h2>
<p>Hi there,</p>
<p>You told us you were interested in ` + program + `, and we're holding an open ` + meeting + ` we'd love you to come to.</p>
<p><strong>What:</strong> Open house — meet the leaders, see what we do<br>
<strong>When:</strong> [date and time]<br>
<strong>Where:</strong> [location]<br>
<strong>Bring:</strong> Nothing but yourselves</p>
<p>Families are welcome to stay for the whole meeting. There's no cost and no obligation to sign up.</p>
<p>Hope to see you there,<br>
` + unitName + `</p>`,
		},
		{
			Name:    "Sign-up night reminder",
			Subject: "Sign-up night is coming — " + unitName,
			Body: `<h2>Sign-up night</h2>
<p>Hi there,</p>
<p>Registration for the new program year is open, and we wanted to make sure you knew.</p>
<p><strong>Sign-up night:</strong> [date and time]<br>
<strong>Where:</strong> [location]</p>
<p>Bring ` + youth + `, and we'll take care of the paperwork on the night. If that date doesn't work for you, reply to this email and we'll sort something out.</p>
<p>Yours in Scouting,<br>
` + unitName + `</p>`,
		},
	}
}
