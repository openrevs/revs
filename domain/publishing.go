package domain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kalafut/dmrs/sqlc"
)

func documentFromSqlc(d sqlc.Document) *Document {
	return &Document{
		Userid:       d.Userid,
		Rev:          d.Rev,
		Hash:         d.Hash,
		Size:         d.Size,
		ReceivedTime: d.ReceivedTime,
	}
}

func epochFromSqlc(e sqlc.Epoch) *Epoch {
	return &Epoch{
		Epoch:        e.Epoch,
		Kind:         e.Kind,
		ManifestHash: e.ManifestHash,
		Created:      e.Created,
	}
}

func (d *DAL) AddDocument(ctx context.Context, doc *Document) error {
	return d.q.AddDocument(ctx, sqlc.AddDocumentParams{
		Userid:       doc.Userid,
		Rev:          doc.Rev,
		Hash:         doc.Hash,
		Size:         doc.Size,
		ReceivedTime: doc.ReceivedTime,
	})
}

// LatestDocument returns the latest accepted document for a user, or nil
// if the user has none.
func (d *DAL) LatestDocument(ctx context.Context, userid string) (*Document, error) {
	doc, err := d.q.LatestDocumentByUser(ctx, userid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return documentFromSqlc(doc), nil
}

func (d *DAL) Document(ctx context.Context, userid string, rev int) (*Document, error) {
	doc, err := d.q.DocumentByUserRev(ctx, sqlc.DocumentByUserRevParams{
		Userid: userid,
		Rev:    rev,
	})
	if err != nil {
		return nil, err
	}

	return documentFromSqlc(doc), nil
}

// LatestDocuments returns each user's latest accepted document.
func (d *DAL) LatestDocuments(ctx context.Context) ([]*Document, error) {
	rows, err := d.q.LatestDocuments(ctx)
	if err != nil {
		return nil, err
	}

	docs := make([]*Document, 0, len(rows))
	for _, row := range rows {
		docs = append(docs, documentFromSqlc(row))
	}

	return docs, nil
}

// PublishedDocuments returns the documents referenced by published_head,
// newest-first by received time.
func (d *DAL) PublishedDocuments(ctx context.Context) ([]*Document, error) {
	rows, err := d.q.PublishedDocuments(ctx)
	if err != nil {
		return nil, err
	}

	docs := make([]*Document, 0, len(rows))
	for _, row := range rows {
		docs = append(docs, documentFromSqlc(row))
	}

	return docs, nil
}

// PublishedHeads returns the published rev per user, keyed by user id.
func (d *DAL) PublishedHeads(ctx context.Context) (map[string]int, error) {
	rows, err := d.q.PublishedHeads(ctx)
	if err != nil {
		return nil, err
	}

	heads := make(map[string]int, len(rows))
	for _, row := range rows {
		heads[row.Userid] = row.Rev
	}

	return heads, nil
}

// LatestPatchEpoch returns the most recent patch epoch, or nil if no cut
// has happened yet.
func (d *DAL) LatestPatchEpoch(ctx context.Context) (*Epoch, error) {
	e, err := d.q.LatestPatchEpoch(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return epochFromSqlc(e), nil
}

func (d *DAL) Epochs(ctx context.Context) ([]*Epoch, error) {
	rows, err := d.q.AllEpochs(ctx)
	if err != nil {
		return nil, err
	}

	epochs := make([]*Epoch, 0, len(rows))
	for _, row := range rows {
		epochs = append(epochs, epochFromSqlc(row))
	}

	return epochs, nil
}

func (d *DAL) AddEpoch(ctx context.Context, e *Epoch) error {
	return d.q.AddEpoch(ctx, sqlc.AddEpochParams{
		Epoch:        e.Epoch,
		Kind:         e.Kind,
		ManifestHash: e.ManifestHash,
		Created:      e.Created,
	})
}

// RecordCut atomically records a patch epoch and advances the published
// heads it shipped.
func (d *DAL) RecordCut(ctx context.Context, e *Epoch, heads []PublishedHead) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	q := d.q.WithTx(tx)

	if err := q.AddEpoch(ctx, sqlc.AddEpochParams{
		Epoch:        e.Epoch,
		Kind:         e.Kind,
		ManifestHash: e.ManifestHash,
		Created:      e.Created,
	}); err != nil {
		return fmt.Errorf("recording epoch: %w", err)
	}

	for _, h := range heads {
		if err := q.UpsertPublishedHead(ctx, sqlc.UpsertPublishedHeadParams{
			Userid: h.Userid,
			Rev:    h.Rev,
		}); err != nil {
			return fmt.Errorf("updating published head: %w", err)
		}
	}

	return tx.Commit()
}
