package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/config"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ErrPackRevisionConflict is returned when a pack write does not match the
// revision its author read — somebody else changed the pack in between. The
// caller re-reads and decides; nothing has been written.
var ErrPackRevisionConflict = errors.New("the Playbook changed since it was read")

// PackRevisionHistoryMax is how many entries a pack's revision log keeps,
// from PLAYBOOK_REVISION_HISTORY_MAX or the model's default. The trim happens
// inside the same write that appends, so this is a bound on the document, not
// a cleanup job.
func PackRevisionHistoryMax() int {
	n := config.GetEnvAsInt("PLAYBOOK_REVISION_HISTORY_MAX", models.DefaultPackRevisionHistoryMax)
	if n <= 0 {
		return models.DefaultPackRevisionHistoryMax
	}
	return n
}

// PackRevisionPin is the filter clause that pins a pack write to the revision
// its author read, so a write that would overwrite somebody else's change
// matches nothing instead.
//
// Revision 0 needs the nil branch: a pack written before the field existed has
// no `revision` at all, and Mongo's equality on 0 does not match a missing
// field. Without it the first chat-driven or hand edit of every existing pack
// would report a conflict that is not one.
func PackRevisionPin(atRevision int) any {
	if atRevision <= 0 {
		return bson.M{"$in": []any{nil, 0}}
	}
	return atRevision
}

// AddPackRevision merges into update the operators that record one revision of
// a pack: the bump of `revision`, and the bounded append of entry to
// `revisions`. The caller supplies its own content operators in the same
// update and pins its filter with PackRevisionPin(atRevision).
//
// Every writer of pack content goes through this — the Playbook editor's PUT
// here, and the enterprise analysis-area applier — so the content change, the
// version bump and the log entry are always one atomic write, and a log can
// never describe a pack it did not land on. Doing it in two writes is how a
// history ends up claiming a change that failed.
func AddPackRevision(update bson.M, atRevision int, entry models.PackRevision, historyMax int) {
	if historyMax <= 0 {
		historyMax = models.DefaultPackRevisionHistoryMax
	}
	entry.Revision = atRevision + 1
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}

	inc, _ := update["$inc"].(bson.M)
	if inc == nil {
		inc = bson.M{}
		update["$inc"] = inc
	}
	inc["revision"] = 1

	push, _ := update["$push"].(bson.M)
	if push == nil {
		push = bson.M{}
		update["$push"] = push
	}
	// $each + a negative $slice keeps the newest historyMax entries, trimming
	// oldest-first as part of this write rather than later.
	push["revisions"] = bson.M{
		"$each":  bson.A{entry},
		"$slice": -historyMax,
	}
}

// DomainPackRepository provides CRUD access to the domain_packs collection.
type DomainPackRepository struct {
	col *mongo.Collection
}

func NewDomainPackRepository(db *DB) *DomainPackRepository {
	return &DomainPackRepository{col: db.Collection("domain_packs")}
}

func (r *DomainPackRepository) Create(ctx context.Context, pack *models.DomainPack) error {
	pack.CreatedAt = time.Now()
	pack.UpdatedAt = time.Now()
	// A new pack is revision 1 with no log entry: creating a pack is not a
	// change to one, and there is no "before" to record. Anything the caller
	// carried in these fields (an imported pack, a pack round-tripped through
	// a form) is somebody else's history and is dropped here.
	pack.Revision = 1
	pack.Revisions = nil

	result, err := r.col.InsertOne(ctx, pack)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return fmt.Errorf("domain pack with slug %q already exists", pack.Slug)
		}
		return fmt.Errorf("insert domain pack: %w", err)
	}

	if oid, ok := result.InsertedID.(primitive.ObjectID); ok {
		pack.ID = oid.Hex()
	}

	return nil
}

func (r *DomainPackRepository) GetBySlug(ctx context.Context, slug string) (*models.DomainPack, error) {
	var pack models.DomainPack
	if err := r.col.FindOne(ctx, bson.M{"slug": slug}).Decode(&pack); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("find domain pack: %w", err)
	}
	return &pack, nil
}

func (r *DomainPackRepository) GetByID(ctx context.Context, id string) (*models.DomainPack, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, nil
	}

	var pack models.DomainPack
	if err := r.col.FindOne(ctx, bson.M{"_id": oid}).Decode(&pack); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("find domain pack: %w", err)
	}
	return &pack, nil
}

func (r *DomainPackRepository) List(ctx context.Context, publishedOnly bool) ([]*models.DomainPack, error) {
	filter := bson.M{}
	if publishedOnly {
		filter["is_published"] = true
	}

	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	cursor, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list domain packs: %w", err)
	}
	defer cursor.Close(ctx)

	packs := make([]*models.DomainPack, 0)
	if err := cursor.All(ctx, &packs); err != nil {
		return nil, fmt.Errorf("decode domain packs: %w", err)
	}
	return packs, nil
}

// Update replaces a pack's content by slug.
//
// rev records the change in the pack's revision log and bumps its revision, in
// the same write as the content, pinned on atRevision — the revision the
// caller read the pack at. Pass rev == nil only when the content is known not
// to have changed (a save of an untouched form): the write then neither bumps
// nor logs, because a revision that changed nothing is noise in a history
// somebody has to read.
//
// The revision fields are stripped from pack regardless of what the caller
// put there. The Playbook editor round-trips a whole pack through a form and
// sends it back, so trusting the body would let a stale copy — or an empty one
// — wipe the log. Same defence the line above applies to the immutable _id.
func (r *DomainPackRepository) Update(ctx context.Context, slug string, pack *models.DomainPack, atRevision int, rev *models.PackRevision) error {
	pack.ID = ""
	pack.UpdatedAt = time.Now()
	pack.Revision = 0
	pack.Revisions = nil

	filter := bson.M{"slug": slug}
	update := bson.M{"$set": pack}
	if rev != nil {
		if rev.Origin == "" {
			return fmt.Errorf("update domain pack %s: a revision must say where it came from", slug)
		}
		filter["revision"] = PackRevisionPin(atRevision)
		AddPackRevision(update, atRevision, *rev, PackRevisionHistoryMax())
	}

	result, err := r.col.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update domain pack: %w", err)
	}
	if result.MatchedCount == 0 {
		// Either the pack is gone or its revision moved under us. Tell the
		// two apart, so a concurrent edit does not report as a missing pack.
		if rev != nil {
			if n, cerr := r.col.CountDocuments(ctx, bson.M{"slug": slug}); cerr == nil && n > 0 {
				return ErrPackRevisionConflict
			}
		}
		return fmt.Errorf("domain pack not found: %s", slug)
	}
	return nil
}

func (r *DomainPackRepository) Delete(ctx context.Context, slug string) error {
	result, err := r.col.DeleteOne(ctx, bson.M{"slug": slug})
	if err != nil {
		return fmt.Errorf("delete domain pack: %w", err)
	}
	if result.DeletedCount == 0 {
		return fmt.Errorf("domain pack not found: %s", slug)
	}
	return nil
}
