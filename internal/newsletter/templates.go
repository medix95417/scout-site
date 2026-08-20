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
