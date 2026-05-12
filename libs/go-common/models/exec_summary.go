package models

import "time"

// ExecutiveSummary is the structured newspaper-style report produced
// for a single discovery run. One document per (discovery_id, version)
// in the executive_summaries collection.
//
// The community repo defines the model so the dashboard build can
// resolve the type when an enterprise overlay introduces the
// rendering UI. The generator, store, handlers, and rendering logic
// all live in the enterprise repo — the community build never writes
// or reads this collection.
//
// Versioning: every regeneration writes a new document with the same
// (project_id, discovery_id) and a fresh Version. The latest version
// is what the dashboard renders by default; older versions remain
// readable via ?version=N.
type ExecutiveSummary struct {
	ID            string    `bson:"_id,omitempty" json:"id"`
	ProjectID     string    `bson:"project_id" json:"project_id"`
	DiscoveryID   string    `bson:"discovery_id" json:"discovery_id"`
	Version       int       `bson:"version" json:"version"`
	Language      string    `bson:"language" json:"language"`
	Model         string    `bson:"model" json:"model"`
	PromptVersion string    `bson:"prompt_version,omitempty" json:"prompt_version,omitempty"`

	GeneratedAt time.Time `bson:"generated_at" json:"generated_at"`
	GeneratedBy string    `bson:"generated_by" json:"generated_by"`
	TokensUsed  int       `bson:"tokens_used,omitempty" json:"tokens_used,omitempty"`
	DurationMS  int64     `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`

	// Status is one of:
	//   "generating" — generator running, document is a placeholder
	//   "ready"      — document is complete and renderable
	//   "failed"     — generator gave up; Error explains why
	Status string `bson:"status" json:"status"`
	Error  string `bson:"error,omitempty" json:"error,omitempty"`

	Issue    IssueMeta       `bson:"issue" json:"issue"`
	Lead     LeadSection     `bson:"lead" json:"lead"`
	Sections []ThemedSection `bson:"sections,omitempty" json:"sections,omitempty"`
	Action   ActionSection   `bson:"action" json:"action"`
	About    AboutSection    `bson:"about" json:"about"`

	// CitedInsightIDs / CitedRecIDs are the union of every citation
	// the body of the report carries. The generator populates them
	// after parsing the LLM output so the stale-citation sweep can
	// detect a report referencing an insight or recommendation that
	// has since been deleted without re-parsing every paragraph.
	CitedInsightIDs []string `bson:"cited_insight_ids,omitempty" json:"cited_insight_ids,omitempty"`
	CitedRecIDs     []string `bson:"cited_rec_ids,omitempty" json:"cited_rec_ids,omitempty"`
}

// IssueMeta drives the masthead and "by the numbers" header strip.
type IssueMeta struct {
	Title               string `bson:"title" json:"title"`
	IssueLabel          string `bson:"issue_label" json:"issue_label"`
	DateLabel           string `bson:"date_label" json:"date_label"`
	ScanCount           int    `bson:"scan_count" json:"scan_count"`
	InsightCount        int    `bson:"insight_count" json:"insight_count"`
	RecommendationCount int    `bson:"recommendation_count" json:"recommendation_count"`
}

// LeadSection is the "front page" — one headline article, a
// supporting sidebar, a stat row, and three sub-stories pointing into
// the themed section pages.
type LeadSection struct {
	Kicker         string      `bson:"kicker,omitempty" json:"kicker,omitempty"`
	Headline       string      `bson:"headline" json:"headline"`
	HeadlineAccent string      `bson:"headline_accent,omitempty" json:"headline_accent,omitempty"`
	Deck           string      `bson:"deck,omitempty" json:"deck,omitempty"`
	Article        []Paragraph `bson:"article,omitempty" json:"article,omitempty"`
	PullQuote      string      `bson:"pull_quote,omitempty" json:"pull_quote,omitempty"`
	Sidebar        Sidebar     `bson:"sidebar" json:"sidebar"`
	StatRow        []Stat      `bson:"stat_row,omitempty" json:"stat_row,omitempty"`
	Stories        []Story     `bson:"stories,omitempty" json:"stories,omitempty"`
}

// ThemedSection is one interior page of the report — typically one
// analysis area (inventory health, monetisation, retention, …).
type ThemedSection struct {
	Slug         string      `bson:"slug" json:"slug"`
	NavTitle     string      `bson:"nav_title" json:"nav_title"`
	AnalysisArea string      `bson:"analysis_area,omitempty" json:"analysis_area,omitempty"`
	Kicker       string      `bson:"kicker,omitempty" json:"kicker,omitempty"`
	Headline     string      `bson:"headline" json:"headline"`
	Deck         string      `bson:"deck,omitempty" json:"deck,omitempty"`
	Article      []Paragraph `bson:"article,omitempty" json:"article,omitempty"`
	PullQuote    string      `bson:"pull_quote,omitempty" json:"pull_quote,omitempty"`
	Sidebar      Sidebar     `bson:"sidebar" json:"sidebar"`
	StatRow      []Stat      `bson:"stat_row,omitempty" json:"stat_row,omitempty"`
	SubStories   []SubStory  `bson:"sub_stories,omitempty" json:"sub_stories,omitempty"`
}

// ActionSection is the closing "what to do this week / month /
// quarter" checklist. Items reference the underlying recommendations
// via Citations.
type ActionSection struct {
	Headline   string            `bson:"headline" json:"headline"`
	Deck       string            `bson:"deck,omitempty" json:"deck,omitempty"`
	Timeframes []ActionTimeframe `bson:"timeframes,omitempty" json:"timeframes,omitempty"`
}

// ActionTimeframe groups action items by urgency.
type ActionTimeframe struct {
	Label    string       `bson:"label" json:"label"`
	Subtitle string       `bson:"subtitle,omitempty" json:"subtitle,omitempty"`
	Items    []ActionItem `bson:"items,omitempty" json:"items,omitempty"`
}

// ActionItem is one bullet inside an ActionTimeframe.
type ActionItem struct {
	Title     string     `bson:"title" json:"title"`
	Body      string     `bson:"body,omitempty" json:"body,omitempty"`
	Citations []Citation `bson:"citations,omitempty" json:"citations,omitempty"`
}

// AboutSection is the closing "how we generated this" explainer.
type AboutSection struct {
	Headline  string      `bson:"headline,omitempty" json:"headline,omitempty"`
	Deck      string      `bson:"deck,omitempty" json:"deck,omitempty"`
	Body      []Paragraph `bson:"body,omitempty" json:"body,omitempty"`
	PullQuote string      `bson:"pull_quote,omitempty" json:"pull_quote,omitempty"`
}

// Paragraph holds one block of body prose. Text contains inline
// citation tokens of the form {{I:insight-id}} for insights and
// {{R:rec-id}} for recommendations; the dashboard resolves them to
// numbered links at render time. Multiple IDs separated by commas
// in one token (`{{I:a,b}}`) render as a single grouped citation.
//
// Lede=true asks the renderer to apply a drop-cap-style first-letter
// treatment, used for the opening paragraph of an article.
type Paragraph struct {
	Text string `bson:"text" json:"text"`
	Lede bool   `bson:"lede,omitempty" json:"lede,omitempty"`
}

// Sidebar holds the optional "critical" and "brand" cards adjacent
// to a lead article. Either or both may be nil.
type Sidebar struct {
	CriticalCard *Card `bson:"critical_card,omitempty" json:"critical_card,omitempty"`
	BrandCard    *Card `bson:"brand_card,omitempty" json:"brand_card,omitempty"`
}

// Card is one rectangular sidebar callout. Items are bulleted
// label / sub-line pairs; BigStats are large-number callouts for
// stand-alone figures inside the card.
type Card struct {
	Eyebrow  string     `bson:"eyebrow,omitempty" json:"eyebrow,omitempty"`
	Items    []CardItem `bson:"items,omitempty" json:"items,omitempty"`
	BigStats []BigStat  `bson:"big_stats,omitempty" json:"big_stats,omitempty"`
}

// CardItem is one bulleted entry inside a Card.
type CardItem struct {
	Bold      string     `bson:"bold" json:"bold"`
	Sub       string     `bson:"sub,omitempty" json:"sub,omitempty"`
	Citations []Citation `bson:"citations,omitempty" json:"citations,omitempty"`
}

// BigStat is a large-number callout inside a sidebar Card.
type BigStat struct {
	Value     string     `bson:"value" json:"value"`
	Sub       string     `bson:"sub,omitempty" json:"sub,omitempty"`
	Citations []Citation `bson:"citations,omitempty" json:"citations,omitempty"`
}

// Stat is one cell of a 4-up stat row above an article.
type Stat struct {
	Value     string     `bson:"value" json:"value"`
	Label     string     `bson:"label" json:"label"`
	Severity  string     `bson:"severity,omitempty" json:"severity,omitempty"`
	Citations []Citation `bson:"citations,omitempty" json:"citations,omitempty"`
}

// Story is one of the three lead-page sub-stories that point into a
// themed section page. RefSlug matches a ThemedSection.Slug; RefLabel
// is the user-visible "Page 03 · Showcase" label.
type Story struct {
	Kicker    string      `bson:"kicker,omitempty" json:"kicker,omitempty"`
	Headline  string      `bson:"headline" json:"headline"`
	Deck      string      `bson:"deck,omitempty" json:"deck,omitempty"`
	Body      []Paragraph `bson:"body,omitempty" json:"body,omitempty"`
	RefSlug   string      `bson:"ref_slug,omitempty" json:"ref_slug,omitempty"`
	RefLabel  string      `bson:"ref_label,omitempty" json:"ref_label,omitempty"`
	Citations []Citation  `bson:"citations,omitempty" json:"citations,omitempty"`
}

// SubStory is a 2-up cell inside a themed section. The renderer
// chooses between a free-form Body and a structured BarList based
// on which slice is populated; they are not mutually exclusive
// (some sub-stories carry both prose and a quick bar chart).
type SubStory struct {
	Kicker    string      `bson:"kicker,omitempty" json:"kicker,omitempty"`
	Headline  string      `bson:"headline" json:"headline"`
	Body      []Paragraph `bson:"body,omitempty" json:"body,omitempty"`
	BarList   []BarItem   `bson:"bar_list,omitempty" json:"bar_list,omitempty"`
	Citations []Citation  `bson:"citations,omitempty" json:"citations,omitempty"`
}

// BarItem is one row of a horizontal-bar visualisation inside a
// SubStory. BarPercent is the bar width as a 0-100 integer; Severity
// drives the bar colour ("critical" / "warn" / "ok" / "").
type BarItem struct {
	Label      string     `bson:"label" json:"label"`
	Value      string     `bson:"value" json:"value"`
	Severity   string     `bson:"severity,omitempty" json:"severity,omitempty"`
	BarPercent int        `bson:"bar_percent" json:"bar_percent"`
	Citations  []Citation `bson:"citations,omitempty" json:"citations,omitempty"`
}

// Citation is a single back-reference to an insight or
// recommendation. The dashboard renderer assigns a number to each
// unique (Type, ID) pair in reading order across the document.
type Citation struct {
	// Type is "insight" or "recommendation".
	Type string `bson:"type" json:"type"`
	// ID is the insight or recommendation _id the citation resolves to.
	ID string `bson:"id" json:"id"`
}
