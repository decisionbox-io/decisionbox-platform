package database

import (
	"context"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// ProjectRepo abstracts project CRUD operations for handler unit testing.
type ProjectRepo interface {
	Create(ctx context.Context, p *models.Project) error
	GetByID(ctx context.Context, id string) (*models.Project, error)
	List(ctx context.Context, limit, offset int) ([]*models.Project, error)
	Update(ctx context.Context, id string, p *models.Project) error
	Delete(ctx context.Context, id string) error
	Count(ctx context.Context) (int, error)
	CountWithWarehouse(ctx context.Context) (int, error)
}

// DiscoveryRepo abstracts discovery read operations for handler unit testing.
type DiscoveryRepo interface {
	GetByID(ctx context.Context, id string) (*models.DiscoveryResult, error)
	GetLatest(ctx context.Context, projectID string) (*models.DiscoveryResult, error)
	GetByDate(ctx context.Context, projectID string, date time.Time) (*models.DiscoveryResult, error)
	List(ctx context.Context, projectID string, limit int) ([]*models.DiscoveryResult, error)
}

// RunRepo abstracts discovery run operations for handler unit testing.
type RunRepo interface {
	Create(ctx context.Context, projectID string) (string, error)
	GetByID(ctx context.Context, runID string) (*models.DiscoveryRun, error)
	GetLatestByProject(ctx context.Context, projectID string) (*models.DiscoveryRun, error)
	GetRunningByProject(ctx context.Context, projectID string) (*models.DiscoveryRun, error)
	Fail(ctx context.Context, runID string, errMsg string) error
	Cancel(ctx context.Context, runID string) error
	SetPolicyReservationID(ctx context.Context, runID, reservationID string) error
	ListTerminalWithReservation(ctx context.Context, limit int) ([]*models.DiscoveryRun, error)
	ClearPolicyReservationID(ctx context.Context, runID string) error
}

// FeedbackRepo abstracts feedback operations for handler unit testing.
type FeedbackRepo interface {
	Upsert(ctx context.Context, fb *models.Feedback) (*models.Feedback, error)
	ListByDiscovery(ctx context.Context, discoveryID string) ([]*models.Feedback, error)
	Delete(ctx context.Context, id string) error
}

// PricingRepo abstracts pricing operations for handler unit testing.
type PricingRepo interface {
	Get(ctx context.Context) (*models.Pricing, error)
	Save(ctx context.Context, pricing *models.Pricing) error
}

// InsightRepo abstracts insight operations for handler unit testing.
type InsightRepo interface {
	Create(ctx context.Context, insight *commonmodels.StandaloneInsight) error
	CreateMany(ctx context.Context, insights []*commonmodels.StandaloneInsight) error
	GetByID(ctx context.Context, id string) (*commonmodels.StandaloneInsight, error)
	ListByProject(ctx context.Context, projectID string, limit, offset int) ([]*commonmodels.StandaloneInsight, error)
	ListByDiscovery(ctx context.Context, discoveryID string) ([]*commonmodels.StandaloneInsight, error)
	CountByProject(ctx context.Context, projectID string) (int64, error)
	UpdateEmbedding(ctx context.Context, id string, embeddingText, embeddingModel string) error
	UpdateDuplicate(ctx context.Context, id string, duplicateOf string, score float64) error
	GetLatestEmbeddingModel(ctx context.Context, projectID string) (string, error)
}

// RecommendationRepo abstracts recommendation operations for handler unit testing.
type RecommendationRepo interface {
	Create(ctx context.Context, rec *commonmodels.StandaloneRecommendation) error
	CreateMany(ctx context.Context, recs []*commonmodels.StandaloneRecommendation) error
	GetByID(ctx context.Context, id string) (*commonmodels.StandaloneRecommendation, error)
	ListByProject(ctx context.Context, projectID string, limit, offset int) ([]*commonmodels.StandaloneRecommendation, error)
	ListByDiscovery(ctx context.Context, discoveryID string) ([]*commonmodels.StandaloneRecommendation, error)
	CountByProject(ctx context.Context, projectID string) (int64, error)
	UpdateEmbedding(ctx context.Context, id string, embeddingText, embeddingModel string) error
	UpdateDuplicate(ctx context.Context, id string, duplicateOf string, score float64) error
}

// SearchHistoryRepo abstracts search history operations.
type SearchHistoryRepo interface {
	Save(ctx context.Context, entry *commonmodels.SearchHistory) error
	ListByUser(ctx context.Context, userID string, limit int) ([]*commonmodels.SearchHistory, error)
	ListByProject(ctx context.Context, projectID string, limit int) ([]*commonmodels.SearchHistory, error)
}

// AskSessionRepo abstracts ask session (conversation) operations.
type AskSessionRepo interface {
	Create(ctx context.Context, session *commonmodels.AskSession) error
	AppendMessage(ctx context.Context, sessionID string, msg commonmodels.AskSessionMessage) error
	GetByID(ctx context.Context, sessionID string) (*commonmodels.AskSession, error)
	ListByProject(ctx context.Context, projectID string, limit int) ([]*commonmodels.AskSession, error)
	Delete(ctx context.Context, sessionID string) error
}

// BookmarkListRepo abstracts bookmark list operations for handler unit testing.
type BookmarkListRepo interface {
	Create(ctx context.Context, list *models.BookmarkList) error
	GetByID(ctx context.Context, projectID, userID, listID string) (*models.BookmarkList, error)
	List(ctx context.Context, projectID, userID string) ([]*models.BookmarkList, error)
	Update(ctx context.Context, projectID, userID, listID string, patch UpdateFields) (*models.BookmarkList, error)
	Delete(ctx context.Context, projectID, userID, listID string) error
}

// BookmarkRepo abstracts bookmark operations for handler unit testing.
type BookmarkRepo interface {
	Add(ctx context.Context, bm *models.Bookmark) (*models.Bookmark, error)
	ListByList(ctx context.Context, listID string) ([]*models.Bookmark, error)
	Delete(ctx context.Context, projectID, userID, listID, bookmarkID string) error
	ListsContaining(ctx context.Context, projectID, userID, targetType, targetID string) ([]string, error)
}

// ReadMarkRepo abstracts read-state operations for handler unit testing.
type ReadMarkRepo interface {
	Upsert(ctx context.Context, mark *models.ReadMark) error
	Delete(ctx context.Context, projectID, userID, targetType, targetID string) error
	ListReadIDs(ctx context.Context, projectID, userID, targetType string) ([]string, error)
}

// DomainPackRepo abstracts domain pack CRUD operations for handler unit testing.
type DomainPackRepo interface {
	Create(ctx context.Context, pack *models.DomainPack) error
	GetBySlug(ctx context.Context, slug string) (*models.DomainPack, error)
	GetByID(ctx context.Context, id string) (*models.DomainPack, error)
	List(ctx context.Context, publishedOnly bool) ([]*models.DomainPack, error)
	Update(ctx context.Context, slug string, pack *models.DomainPack) error
	Delete(ctx context.Context, slug string) error
}

// Compile-time checks: concrete repos satisfy interfaces.
var (
	_ ProjectRepo        = (*ProjectRepository)(nil)
	_ DiscoveryRepo      = (*DiscoveryRepository)(nil)
	_ RunRepo            = (*RunRepository)(nil)
	_ FeedbackRepo       = (*FeedbackRepository)(nil)
	_ PricingRepo        = (*PricingRepository)(nil)
	_ InsightRepo        = (*InsightRepository)(nil)
	_ RecommendationRepo = (*RecommendationRepository)(nil)
	_ SearchHistoryRepo  = (*SearchHistoryRepository)(nil)
	_ AskSessionRepo     = (*AskSessionRepository)(nil)
	_ DomainPackRepo     = (*DomainPackRepository)(nil)
	_ BookmarkListRepo   = (*BookmarkListRepository)(nil)
	_ BookmarkRepo       = (*BookmarkRepository)(nil)
	_ ReadMarkRepo       = (*ReadMarkRepository)(nil)
)
