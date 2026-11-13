package domain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openrevs/revs/sqlc"
)

func documentFromSqlc(d sqlc.Document) *Document {
	return &Document{
		Userid:       d.Userid,
		Rev:          d.Rev,
		Hash:         d.Hash,
		Size:         d.Size,
		ReceivedTime: d.ReceivedTime,
		Direct:       d.Direct,
	}
}

func documentsFromSqlc(rows []sqlc.Document) []*Document {
	docs := make([]*Document, 0, len(rows))
	for _, row := range rows {
		docs = append(docs, documentFromSqlc(row))
	}
	return docs
}

func (d *DAL) AddDocument(ctx context.Context, doc *Document) error {
	return d.q.AddDocument(ctx, sqlc.AddDocumentParams{
		Userid:       doc.Userid,
		Rev:          doc.Rev,
		Hash:         doc.Hash,
		Size:         doc.Size,
		ReceivedTime: doc.ReceivedTime,
		Direct:       doc.Direct,
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
	return documentsFromSqlc(rows), nil
}

// LatestDirectDocuments returns each user's latest document that was
// uploaded to this server directly.
func (d *DAL) LatestDirectDocuments(ctx context.Context) ([]*Document, error) {
	rows, err := d.q.LatestDirectDocuments(ctx)
	if err != nil {
		return nil, err
	}
	return documentsFromSqlc(rows), nil
}

// ChannelDocuments returns the documents a channel holds at an epoch,
// newest-first by received time. Epoch 0 is the empty channel.
func (d *DAL) ChannelDocuments(ctx context.Context, channel string, epoch int) ([]*Document, error) {
	rows, err := d.q.ChannelDocuments(ctx, sqlc.ChannelDocumentsParams{Channel: channel, Epoch: epoch})
	if err != nil {
		return nil, err
	}
	return documentsFromSqlc(rows), nil
}

// LatestChannelEpoch returns a channel's current epoch, 0 before its
// first cut.
func (d *DAL) LatestChannelEpoch(ctx context.Context, channel string) (int, error) {
	return d.q.LatestChannelEpoch(ctx, channel)
}

func (d *DAL) ChannelArtifacts(ctx context.Context, channel string) ([]*Artifact, error) {
	rows, err := d.q.ChannelArtifacts(ctx, channel)
	if err != nil {
		return nil, err
	}

	arts := make([]*Artifact, 0, len(rows))
	for _, row := range rows {
		arts = append(arts, &Artifact{
			Channel: row.Channel,
			Kind:    row.Kind,
			Start:   row.StartEpoch,
			End:     row.EndEpoch,
			Parts:   row.Parts,
			Created: row.Created,
		})
	}

	return arts, nil
}

// RecordCut atomically commits a channel epoch: the epoch itself, the
// membership changes it makes, and the artifacts published with it.
func (d *DAL) RecordCut(ctx context.Context, channel string, epoch int, created time.Time, changes []ChannelChange, artifacts []*Artifact) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	q := d.q.WithTx(tx)

	if err := q.AddChannelEpoch(ctx, sqlc.AddChannelEpochParams{
		Channel: channel,
		Epoch:   epoch,
		Created: created,
	}); err != nil {
		return fmt.Errorf("recording epoch: %w", err)
	}

	for _, c := range changes {
		if err := q.AddChannelChange(ctx, sqlc.AddChannelChangeParams{
			Channel: channel,
			Epoch:   epoch,
			Userid:  c.Userid,
			Rev:     c.Rev,
		}); err != nil {
			return fmt.Errorf("recording channel change: %w", err)
		}
	}

	for _, a := range artifacts {
		if err := q.AddArtifact(ctx, sqlc.AddArtifactParams{
			Channel:    channel,
			Kind:       a.Kind,
			StartEpoch: a.Start,
			EndEpoch:   a.End,
			Parts:      a.Parts,
			Created:    a.Created,
		}); err != nil {
			return fmt.Errorf("recording artifact: %w", err)
		}
	}

	return tx.Commit()
}
