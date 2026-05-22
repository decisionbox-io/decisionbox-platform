package bigquery

import (
	bq "cloud.google.com/go/bigquery"
)

// bqClient abstracts the BigQuery client API for testing.
// The real implementation is *bq.Client.
//
// Note: Query returns *bq.Query and Dataset returns *bq.Dataset, both of
// which hold an internal reference to the concrete *bq.Client. Full mock-based
// unit testing of query execution requires an emulator or integration test.
// This interface still enables constructor-level injection and close testing.
type bqClient interface {
	Query(q string) *bq.Query
	Dataset(datasetID string) *bq.Dataset
	// DatasetInProject returns a dataset reference scoped to a
	// different GCP project than the one the client was constructed
	// with. Required for the cross-project read pattern: client
	// constructed against the operator's billing/jobs project (where
	// jobs.create is granted), datasets pointed at the project that
	// owns the data (e.g. bigquery-public-data, which is read-only
	// and denies jobs.create to everyone). Real *bq.Client exposes
	// this directly; same return type so call sites are
	// interchangeable.
	DatasetInProject(projectID, datasetID string) *bq.Dataset
	Close() error
}

// Compile-time check that the real client satisfies the interface.
var _ bqClient = (*bq.Client)(nil)
