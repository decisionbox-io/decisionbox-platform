package models

import (
	"reflect"
	"sort"
	"time"

	warehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// DomainPack represents a domain-specific analysis pack stored in MongoDB.
// Domain packs define the prompts, analysis areas, categories, and profile
// schemas used by the AI discovery agent.
type DomainPack struct {
	ID          string `bson:"_id,omitempty" json:"id"`
	Slug        string `bson:"slug" json:"slug"`
	Name        string `bson:"name" json:"name"`
	Description string `bson:"description" json:"description"`
	Version     string `bson:"version" json:"version"`
	Author      string `bson:"author,omitempty" json:"author,omitempty"`
	SourceURL   string `bson:"source_url,omitempty" json:"source_url,omitempty"`
	IsPublished bool   `bson:"is_published" json:"is_published"`

	// Shape is the source shape this pack was written for. A pack is not
	// shape-neutral: its exploration prompt and analysis areas assume a source
	// organised one way, and teaching a generator from a pack written for the
	// other shape produces something that reads right and asks for queries the
	// source cannot answer.
	//
	// Absent means ShapeEntities, via EffectiveShape. Every pack written before
	// this field existed targets a table-shaped source, and a pack for any other
	// shape cannot be authored without saying so — the same direction the
	// provider descriptor's own default takes, for the same reason.
	Shape warehouse.SourceShape `bson:"shape,omitempty" json:"shape,omitempty"`

	Categories    []PackCategory    `bson:"categories" json:"categories"`
	Prompts       PackPrompts       `bson:"prompts" json:"prompts"`
	AnalysisAreas PackAnalysisAreas `bson:"analysis_areas" json:"analysis_areas"`
	ProfileSchema PackProfileSchema `bson:"profile_schema" json:"profile_schema"`

	// Per-pack LLM token usage, summed across every generation attempt
	// (initial + auto-fix retries) the synthesiser made for this pack.
	// Populated by the enterprise pack generator when a pack is first
	// persisted; community filesystem-loaded packs leave it absent.
	// omitempty so packs that pre-date this tracking render as absent
	// rather than zero, preserving the "unknown vs. zero spent"
	// distinction.
	InputTokens  int `bson:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens int `bson:"output_tokens,omitempty" json:"output_tokens,omitempty"`

	// Revision is monotonic and bumped by every write that changes the pack's
	// content. It is also the optimistic-concurrency token every such write
	// pins itself on, so two writers changing different parts of a pack cannot
	// silently overwrite one another.
	//
	// Absent on every pack written before the field existed, which reads as 0:
	// an unknown history rather than a claimed empty one. The first write
	// after that makes it revision 1.
	Revision int `bson:"revision,omitempty" json:"revision,omitempty"`
	// Revisions is the pack's own change log, newest last, bounded to the most
	// recent PackRevisionHistoryMax entries. It lives on the pack rather than
	// in a collection of its own so that reading a pack reads its history and
	// so that the bump, the log entry and the content change are one write.
	//
	// It is deliberately NOT part of the portable export format: it names
	// people and conversations in one deployment, which mean nothing in
	// another.
	Revisions []PackRevision `bson:"revisions,omitempty" json:"revisions,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// How a pack revision came about. These two are the whole set: a pack is
// changed either by someone editing it directly or by someone approving a
// change proposed in a conversation. Generating a pack creates one rather than
// changing one, so it starts at revision 1 with no log entry.
const (
	// PackRevisionOriginUser is a direct edit — the Playbook editor's PUT.
	PackRevisionOriginUser = "user"
	// PackRevisionOriginAskSession is an approved proposal from a Playbook
	// conversation, recorded with the session and proposal it came from.
	PackRevisionOriginAskSession = "ask_session"
)

// DefaultPackRevisionHistoryMax is how many revisions a pack's log keeps when
// PLAYBOOK_REVISION_HISTORY_MAX says nothing. The log is trimmed oldest-first
// in the same write that appends to it, so a pack document cannot grow without
// bound as it is edited.
const DefaultPackRevisionHistoryMax = 50

// PackRevision is one entry in a pack's revision log: when the pack changed,
// who changed it, how the change arrived, and what the areas it touched looked
// like before.
type PackRevision struct {
	// Revision is the pack revision this entry produced, so an entry and the
	// pack's own counter can always be matched up.
	Revision int `bson:"revision" json:"revision"`
	// Origin is one of the PackRevisionOrigin constants.
	Origin string `bson:"origin" json:"origin"`
	// ActorSub is the subject of whoever made the change. Empty only when
	// authentication is off, which is the one case where there is nobody to
	// name.
	ActorSub string `bson:"actor_sub,omitempty" json:"actor_sub,omitempty"`
	// SessionID and ProposalID are set for an ask_session revision: the
	// conversation the change was proposed in, and the proposal that was
	// approved. Together with the proposal row they are the audit trail of a
	// chat-driven edit.
	SessionID  string `bson:"session_id,omitempty" json:"session_id,omitempty"`
	ProposalID string `bson:"proposal_id,omitempty" json:"proposal_id,omitempty"`
	// Summary is one line describing the change, for the history list.
	Summary string `bson:"summary,omitempty" json:"summary,omitempty"`
	// AreaIDs are the analysis areas this revision touched.
	AreaIDs []string `bson:"area_ids,omitempty" json:"area_ids,omitempty"`
	// Before holds those areas exactly as they stood, so the history can say
	// what changed and a removal is recoverable. Only the touched areas are
	// snapshotted, never the pack: a pack runs to hundreds of kilobytes, and
	// copying one per edit is how a document grows until it cannot be read.
	Before []PackAnalysisArea `bson:"before,omitempty" json:"before,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

// ChangedAreaIDs lists the analysis-area ids that differ between two versions
// of a pack — added, removed, or edited — across the base bucket and every
// category bucket. Used to label a revision with the areas it touched.
func ChangedAreaIDs(before, after *DomainPack) []string {
	b, a := flattenAreas(before), flattenAreas(after)
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for id, area := range a {
		prev, existed := b[id]
		if !existed || !reflect.DeepEqual(prev, area) {
			add(id)
		}
	}
	for id := range b {
		if _, still := a[id]; !still {
			add(id)
		}
	}
	sort.Strings(out)
	return out
}

// flattenAreas keys every area in a pack by id. A pack can legally repeat an
// id across buckets; the last one wins here, which is enough for "did this
// area change" and avoids inventing a composite key the rest of the code does
// not use.
func flattenAreas(p *DomainPack) map[string]PackAnalysisArea {
	out := map[string]PackAnalysisArea{}
	if p == nil {
		return out
	}
	for _, a := range p.AnalysisAreas.Base {
		out[a.ID] = a
	}
	for _, areas := range p.AnalysisAreas.Categories {
		for _, a := range areas {
			out[a.ID] = a
		}
	}
	return out
}

// PackAreasByID returns the pack's analysis areas with the given ids, in the
// order asked for, skipping ids the pack does not have. It is how a revision
// entry's Before snapshot is taken: the areas a change touched, as they stood,
// and nothing else.
func PackAreasByID(p *DomainPack, ids []string) []PackAnalysisArea {
	if p == nil || len(ids) == 0 {
		return nil
	}
	byID := flattenAreas(p)
	out := make([]PackAnalysisArea, 0, len(ids))
	for _, id := range ids {
		if a, ok := byID[id]; ok {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// PackContentEqual reports whether two versions of a pack carry the same
// content — everything a reader of the pack would act on, ignoring identity,
// timestamps, token counts and the revision log itself.
//
// It exists so that saving a pack nobody edited does not append a revision:
// a history where half the entries say nothing changed is a history nobody
// reads.
func PackContentEqual(a, b *DomainPack) bool {
	if a == nil || b == nil {
		return a == b
	}
	strip := func(p *DomainPack) DomainPack {
		c := *p
		c.ID = ""
		c.CreatedAt = time.Time{}
		c.UpdatedAt = time.Time{}
		c.Revision = 0
		c.Revisions = nil
		c.InputTokens = 0
		c.OutputTokens = 0
		return c
	}
	sa, sb := strip(a), strip(b)
	return reflect.DeepEqual(sa, sb)
}

// EffectiveShape returns the source shape this pack was written for,
// defaulting to ShapeEntities when the pack declares none.
//
// Read it rather than the field: a pack persisted before shape was recorded
// carries no value, and treating that as "no shape" rather than "the shape
// everything used to be" would exclude the entire existing corpus from any
// decision that branches on it.
func (p DomainPack) EffectiveShape() warehouse.SourceShape {
	if p.Shape != "" {
		return p.Shape
	}
	return warehouse.ShapeEntities
}

// PackCategory is a sub-type within a domain (e.g., "match3" within gaming).
type PackCategory struct {
	ID          string `bson:"id" json:"id"`
	Name        string `bson:"name" json:"name"`
	Description string `bson:"description" json:"description"`
}

// PackPrompts holds all prompt templates organized by base and category.
type PackPrompts struct {
	Base       BasePrompts                `bson:"base" json:"base"`
	Categories map[string]CategoryPrompts `bson:"categories" json:"categories"`
}

// BasePrompts contains the three required prompt templates.
type BasePrompts struct {
	BaseContext     string `bson:"base_context" json:"base_context"`
	Exploration     string `bson:"exploration" json:"exploration"`
	Recommendations string `bson:"recommendations" json:"recommendations"`
}

// CategoryPrompts contains category-specific prompt overrides.
type CategoryPrompts struct {
	ExplorationContext string `bson:"exploration_context,omitempty" json:"exploration_context,omitempty"`
}

// PackAnalysisAreas holds analysis areas organized by base and category.
type PackAnalysisAreas struct {
	Base       []PackAnalysisArea            `bson:"base" json:"base"`
	Categories map[string][]PackAnalysisArea `bson:"categories" json:"categories"`
}

// PackAnalysisArea defines a single analysis area with its inline prompt.
type PackAnalysisArea struct {
	ID          string   `bson:"id" json:"id"`
	Name        string   `bson:"name" json:"name"`
	Description string   `bson:"description" json:"description"`
	Keywords    []string `bson:"keywords" json:"keywords"`
	Priority    int      `bson:"priority" json:"priority"`
	Prompt      string   `bson:"prompt" json:"prompt"`
}

// PackProfileSchema holds JSON Schema for project profile forms.
type PackProfileSchema struct {
	Base       map[string]interface{}            `bson:"base" json:"base"`
	Categories map[string]map[string]interface{} `bson:"categories" json:"categories"`
}
