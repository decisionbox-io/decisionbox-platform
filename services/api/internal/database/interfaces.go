package database

import (
	"context"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/internal/models"
)

// ProjectRepo abstracts project CRUD operations for handler unit testing.
type ProjectRepo interface {
	Create(ctx context.Context, p *models.Project) error
	GetByID(ctx context.Context, id string) (*models.Project, error)
	List(ctx context.Context, limit, offset int) ([]*models.Project, error)
	Update(ctx context.Context, id string, p *models.Project) error
	Delete(ctx context.Context, id string) error
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
	_ ProjectRepo    = (*ProjectRepository)(nil)
	_ DiscoveryRepo  = (*DiscoveryRepository)(nil)
	_ RunRepo        = (*RunRepository)(nil)
	_ FeedbackRepo   = (*FeedbackRepository)(nil)
	_ PricingRepo    = (*PricingRepository)(nil)
	_ DomainPackRepo = (*DomainPackRepository)(nil)
)
