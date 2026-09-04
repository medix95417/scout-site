package help

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// allFeaturesOn is a viewer with every gated feature switched on, so a
// test that cares about capabilities isn't also fighting feature gates.
func allFeaturesOn(caps units.Capabilities, scoutLogin bool) Viewer {
	enabled := map[string]bool{}
	for _, k := range SettingKeys() {
		enabled[k] = true
	}
	return Viewer{Capabilities: caps, IsIndividualScout: scoutLogin, Enabled: enabled}
}

func titles(sections []Section) []string {
	var out []string
	for _, s := range sections {
		for _, t := range s.Topics {
			out = append(out, t.Title)
		}
	}
	return out
}

func bodyOf(sections []Section) string {
	var b strings.Builder
	for _, s := range sections {
		for _, t := range s.Topics {
			b.WriteString(t.Title)
			b.WriteString(" ")
			b.WriteString(strings.Join(t.Body, " "))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestScoutSeesNoTreasuryHelp is the headline rule, and the one the
// requirement was written around: a Scout's login must not be reading
// about the unit's money.
//
// Asserted on the rendered text rather than on topic metadata, because
// the failure that matters is a treasury sentence reaching a Scout's
// screen — however it got there. A topic that describes the ledger but
// forgets to declare RequiredCapability would pass a metadata check and
// fail this one.
func TestScoutSeesNoTreasuryHelp(t *testing.T) {
	scout := allFeaturesOn(units.Capabilities{}, true)
	text := strings.ToLower(bodyOf(For(scout)))

	// "Scout account" is deliberately not in this list: a Scout seeing
	// their own balance is a feature built for them, and its topic is
	// gated on the self-service setting instead.
	forbidden := []string{
		"treasury", "reconcil", "bank statement", "expense", "deposits and withdrawals",
		"authorize", "ledger", "general fund", "committee meeting", "audit",
	}
	for _, word := range forbidden {
		if strings.Contains(text, word) {
			t.Errorf("a Scout's help mentions %q — treasury detail must not reach a Scout login:\n%s", word, bodyOf(For(scout)))
		}
	}
}

// TestFamilyLoginSeesNoTreasuryHelp is the same rule for the ordinary
// shared family login, which holds no capabilities either.
func TestFamilyLoginSeesNoTreasuryHelp(t *testing.T) {
	fam := allFeaturesOn(units.Capabilities{}, false)
	text := strings.ToLower(bodyOf(For(fam)))
	for _, word := range []string{"reconcil", "bank statement", "ledger", "activity log"} {
		if strings.Contains(text, word) {
			t.Errorf("a family login's help mentions %q", word)
		}
	}
}

// TestTreasurerSeesTreasuryHelp is the other half — the gating has to
// actually let the right person through, or "a Scout sees no treasury
// help" would pass trivially by showing nobody anything.
func TestTreasurerSeesTreasuryHelp(t *testing.T) {
	treasurer := allFeaturesOn(units.Capabilities{units.CapManageLedger: true}, false)
	text := strings.ToLower(bodyOf(For(treasurer)))
	for _, word := range []string{"reconcil", "expense", "fundraiser"} {
		if !strings.Contains(text, word) {
			t.Errorf("a Treasurer's help should cover %q but doesn't", word)
		}
	}
}

// TestFeatureOffRemovesItsTopics checks the second rule: help must not
// describe a feature this unit has switched off — checked even for a
// super_admin, who has every capability and would otherwise see
// everything.
func TestFeatureOffRemovesItsTopics(t *testing.T) {
	admin := units.Capabilities{
		units.CapSuperAdmin: true, units.CapEditContent: true,
		units.CapManageLedger: true, units.CapApproveExpenses: true,
	}

	for _, key := range SettingKeys() {
		on := allFeaturesOn(admin, false)
		gated := 0
		for _, topic := range Topics {
			if topicRequires(topic, key) && topic.Visible(on) {
				gated++
			}
		}
		if gated == 0 {
			t.Errorf("setting %q gates no visible topic — either it's dead, or the topic that needs it is hidden for another reason", key)
			continue
		}

		off := on
		off.Enabled = map[string]bool{}
		for k, v := range on.Enabled {
			off.Enabled[k] = v
		}
		off.Enabled[key] = false

		for _, topic := range Topics {
			if topicRequires(topic, key) && topic.Visible(off) {
				t.Errorf("topic %q is still visible with %q switched off", topic.ID, key)
			}
		}
		if len(titles(For(off))) >= len(titles(For(on))) {
			t.Errorf("turning %q off removed no topics at all", key)
		}
	}
}

// TestEveryTopicNamingAGatedFeatureDeclaresItsGate is the drift guard.
//
// The gating only works if each topic declares it, and a topic is just
// prose — nothing stops someone adding a paragraph about newsletters to
// an ungated topic. This scans each topic's own text for the vocabulary
// of a gated feature and requires the matching declaration, so content
// and gating can't quietly come apart.
func TestEveryTopicNamingAGatedFeatureDeclaresItsGate(t *testing.T) {
	// Words that only make sense when a given feature is on. Kept
	// deliberately specific: "account" alone appears in ordinary sign-in
	// prose, so the account topics key off "balance" instead.
	featureWords := map[string][]string{
		settings.TreasuryEnabled:         {"treasury", "reconcil", "fundraiser", "ledger", "expense"},
		settings.AdvancementEnabled:      {"advancement"},
		settings.NewsletterEnabled:       {"newsletter"},
		settings.ScoutAccountSelfService: {"account balance", "my accounts"},
	}

	for _, topic := range Topics {
		text := strings.ToLower(topic.Title + " " + strings.Join(topic.Body, " "))
		for key, words := range featureWords {
			for _, w := range words {
				if !strings.Contains(text, w) {
					continue
				}
				if topicRequires(topic, key) {
					continue
				}
				// A topic may legitimately mention several features when
				// it's about the settings page itself — that one is
				// gated on super_admin and describes the switches.
				if topic.RequiredCapability == units.CapSuperAdmin {
					continue
				}
				t.Errorf("topic %q says %q but doesn't declare RequiresSettings: %q — "+
					"it would still be shown to a unit with that feature switched off",
					topic.ID, w, key)
			}
		}
	}
}

// TestEveryTopicIsReachable catches a topic gated so tightly nobody can
// ever read it — a typo'd capability string, or a combination of gates
// no real login satisfies.
func TestEveryTopicIsReachable(t *testing.T) {
	viewers := []Viewer{
		allFeaturesOn(units.Capabilities{}, false),
		allFeaturesOn(units.Capabilities{}, true),
		allFeaturesOn(units.Capabilities{units.CapEditContent: true}, false),
		allFeaturesOn(units.Capabilities{units.CapManageLedger: true}, false),
		allFeaturesOn(units.Capabilities{units.CapApproveExpenses: true}, false),
		allFeaturesOn(units.Capabilities{units.CapSuperAdmin: true}, false),
	}
	for _, topic := range Topics {
		seen := false
		for _, v := range viewers {
			if topic.Visible(v) {
				seen = true
				break
			}
		}
		if !seen {
			t.Errorf("topic %q is visible to nobody — check its capability and setting gates", topic.ID)
		}
	}
}

// TestTopicIDsAreUniqueAndSlugLike keeps the in-page anchors working.
func TestTopicIDsAreUniqueAndSlugLike(t *testing.T) {
	seen := map[string]bool{}
	for _, topic := range Topics {
		if topic.ID == "" || topic.Title == "" || len(topic.Body) == 0 {
			t.Errorf("topic %+v is missing an ID, title, or body", topic)
		}
		if seen[topic.ID] {
			t.Errorf("duplicate topic ID %q — the page's anchors would collide", topic.ID)
		}
		seen[topic.ID] = true
		if strings.ContainsAny(topic.ID, " _/#") || strings.ToLower(topic.ID) != topic.ID {
			t.Errorf("topic ID %q should be a lowercase hyphenated slug", topic.ID)
		}
	}
}

// topicRequires reports whether a topic is gated on this setting key.
func topicRequires(t Topic, key string) bool {
	for _, k := range t.RequiresSettings {
		if k == key {
			return true
		}
	}
	return false
}
