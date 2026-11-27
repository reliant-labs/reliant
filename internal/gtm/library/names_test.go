// Copyright (c) 2026 Reliant Labs

package library

import (
	"regexp"
	"strings"
	"testing"
	"unicode"
)

// The bank is generic: no company, product or platform names. Say "a
// professional social network", "a launch directory", "a developer forum",
// "a payment processor" instead. Two checks enforce it: a denylist of
// well-known names, and a heuristic that flags any capitalised word that is
// not starting a sentence (brand names are capitalised; generic prose here is
// not), with a short allowlist of acronyms.

// deniedNames are matched case-insensitively on word boundaries. Some are
// also ordinary words (notion, slack, stripe, apple); the bank avoids them
// so the check can stay strict.
var deniedNames = []string{
	// search, social and video networks
	"google", "bing", "yahoo", "duckduckgo", "facebook", "meta", "instagram",
	"whatsapp", "messenger", "linkedin", "twitter", "tweet", "tweets", "reddit", "subreddit",
	"youtube", "tiktok", "snapchat", "pinterest", "quora", "tumblr", "twitch",
	"telegram", "wechat", "bluesky", "mastodon", "threads app",
	// developer and launch communities
	"github", "gitlab", "bitbucket", "stack overflow", "stackoverflow",
	"hacker news", "hackernews", "product hunt", "producthunt", "indie hackers",
	"betalist", "dev.to", "devpost", "mlh",
	// big tech and clouds
	"apple", "amazon", "aws", "microsoft", "azure", "gcp", "ibm", "oracle",
	"openai", "chatgpt", "anthropic", "claude", "gemini", "copilot",
	// SaaS, tools and platforms
	"hubspot", "salesforce", "stripe", "paypal", "slack", "discord", "notion",
	"zapier", "mailchimp", "convertkit", "beehiiv", "substack", "patreon",
	"gumroad", "shopify", "woocommerce", "etsy", "ebay", "airtable",
	"intercom", "zendesk", "figma", "canva", "webflow", "wordpress",
	"squarespace", "wix", "calendly", "typeform", "loom", "zoom", "mixpanel",
	"amplitude", "posthog", "hotjar", "optimizely", "segment.com", "vercel",
	"netlify", "heroku", "crunchbase", "zoominfo", "clearbit", "apollo.io",
	"ahrefs", "semrush", "hootsuite", "eventbrite", "luma", "kickstarter",
	"indiegogo", "appsumo", "upwork", "fiverr", "g2", "capterra",
	"trustpilot", "yelp", "tripadvisor", "n8n", "ifttt",
	// ours
	"reliant", "forge",
}

// xAsPlatform catches "X" used as the platform name ("post on X").
var xAsPlatform = regexp.MustCompile(`\b(on|via|to|from|at|and) X\b|\bX \(formerly\b`)

// allowedCaps are capitalised tokens allowed anywhere: acronyms and units.
var allowedCaps = map[string]bool{
	"SEO": true, "B2B": true, "B2C": true, "API": true, "APIs": true, "CRM": true,
	"ROI": true, "FAQ": true, "FAQs": true, "URL": true, "URLs": true, "SDK": true,
	"CLI": true, "CSV": true, "PDF": true, "SMS": true, "RSS": true, "OSS": true,
	"SaaS": true, "AI": true, "UTM": true, "NPS": true, "MRR": true, "ARR": true,
	"LTV": true, "CAC": true, "CTA": true,
}

// textFields returns every human-readable field of e with a label.
func textFields(e Entry) map[string]string {
	f := map[string]string{
		"key":                strings.ReplaceAll(e.Key, "-", " "),
		"name":               e.Name,
		"summary":            e.Summary,
		"why_it_works":       e.WhyItWorks,
		"suggested_workflow": e.SuggestedWorkflow,
		"tailor_questions":   strings.Join(e.TailorQuestions, "\n"),
		"kpis":               strings.Join(e.KPIs, "\n"),
	}
	return f
}

func denylistPattern() *regexp.Regexp {
	alts := make([]string, len(deniedNames))
	for i, n := range deniedNames {
		alts[i] = regexp.QuoteMeta(n)
	}
	return regexp.MustCompile(`(?i)\b(` + strings.Join(alts, "|") + `)\b`)
}

func TestNoCompanyOrProductNames(t *testing.T) {
	denied := denylistPattern()
	for _, e := range All() {
		for field, text := range textFields(e) {
			if m := denied.FindAllString(text, -1); len(m) > 0 {
				t.Errorf("%s.%s names %q; use a generic description instead", e.Key, field, m)
			}
			if xAsPlatform.MatchString(text) {
				t.Errorf("%s.%s names the platform X; use a generic description instead", e.Key, field)
			}
		}
	}
}

func TestNoCapitalisedNames(t *testing.T) {
	for _, e := range All() {
		for field, text := range textFields(e) {
			if bad := capitalisedTokens(text); len(bad) > 0 {
				t.Errorf("%s.%s has capitalised words %q mid-sentence; brand names are not allowed (or add an acronym to allowedCaps)", e.Key, field, bad)
			}
		}
	}
}

func TestNameChecksCatchExamples(t *testing.T) {
	denied := denylistPattern()
	for _, s := range []string{"Post it on LinkedIn.", "a launch on product hunt", "share in the Discord", "Pay via STRIPE"} {
		if !denied.MatchString(s) {
			t.Errorf("denylist missed %q", s)
		}
	}
	for _, s := range []string{"a professional social network", "a launch directory", "a developer forum", "a payment processor", "metadata and forgery"} {
		if denied.MatchString(s) {
			t.Errorf("denylist flagged generic text %q", s)
		}
	}
	if !xAsPlatform.MatchString("Share the thread on X today") {
		t.Error("xAsPlatform missed \"on X\"")
	}
	for _, s := range []string{"Then post it to SuperCRM.", "Use the Acme dashboard.", "list on a launch directory like Launchly"} {
		if len(capitalisedTokens(s)) == 0 {
			t.Errorf("heuristic missed a name in %q", s)
		}
	}
	for _, s := range []string{"Run a meetup. Then follow up: Send notes.", "Track SEO and B2B leads.\nNext line starts here."} {
		if bad := capitalisedTokens(s); len(bad) > 0 {
			t.Errorf("heuristic flagged %q in %q", bad, s)
		}
	}
}

var wordToken = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*`)

// capitalisedTokens returns the words in text that contain an upper-case
// letter and are neither an allowed acronym nor a plain capitalised word at
// the start of a sentence (start of text, or after . ! ? : or a newline).
//
// Known gaps, so a reviewer still reads new entries:
//   - A plainly capitalised brand that starts a sentence ("Acme makes it
//     easy.") passes, since it looks like any sentence-initial word. A
//     CamelCase or all-caps brand there is still flagged.
//   - A brand written in lower case ("post it on acme") passes unless it is
//     in deniedNames; only the denylist catches lower-case names, and it
//     knows only the names listed.
//   - An all-caps name that is also in allowedCaps passes by design.
func capitalisedTokens(text string) []string {
	var bad []string
	prevEnd := 0
	for i, loc := range wordToken.FindAllStringIndex(text, -1) {
		tok := text[loc[0]:loc[1]]
		sep := text[prevEnd:loc[0]]
		prevEnd = loc[1]
		if !strings.ContainsFunc(tok, unicode.IsUpper) || allowedCaps[tok] {
			continue
		}
		sentenceStart := i == 0 || strings.ContainsAny(sep, ".!?:\n")
		if sentenceStart && !strings.ContainsFunc(tok[1:], unicode.IsUpper) {
			continue
		}
		bad = append(bad, tok)
	}
	return bad
}
