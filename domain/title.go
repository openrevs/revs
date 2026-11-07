package domain

import (
	"context"

	"github.com/kalafut/dmrs/sqlc"
	"github.com/kalafut/dmrs/util"
)

func (u *DAL) TitleSearch(ctx context.Context, q TitleQuery) ([]*Title, error) {
	ts, err := u.q.Titles(ctx, "%"+util.StrFromPtr(q.Name)+"%")
	if err != nil {
		return nil, err
	}

	titles := make([]*Title, 0, len(ts))
	for _, t := range ts {
		titles = append(titles, titleFromSqlc(t))
	}

	return titles, nil
}

func (u *DAL) TitleStats(ctx context.Context, id int) (*TitleStats, error) {
	count, err := u.q.TitleRatingCount(ctx, id)
	if err != nil {
		return nil, err
	}

	avg, err := u.q.TitleRatingAvg(ctx, id)
	if err != nil {
		return nil, err
	}

	stats := &TitleStats{
		RatingCount: count,
	}
	if avg != nil {
		stats.AverageRating = float32(*avg)
	}

	return stats, nil
}

func (u *DAL) Title(ctx context.Context, id int) (*Title, error) {
	title, err := u.q.Title(ctx, id)
	if err != nil {
		return nil, err
	}

	return titleFromSqlc(title), nil
}

func (u *DAL) AddTitle(ctx context.Context, title *Title) error {
	id, err := u.q.AddTitle(ctx, sqlc.AddTitleParams{
		Type:          title.Type,
		Name:          title.Name,
		Year:          title.Year,
		TmdbID:        title.TMDBID,
		TmdbPosterUrl: title.TMDBPosterURL,
	})
	if err != nil {
		return err
	}

	title.ID = int(id)

	return nil
}

func (t *DAL) TitleRatings(ctx context.Context, id int) ([]*Rating, error) {
	rows, err := t.q.TitleRatings(ctx, id)
	if err != nil {
		return nil, err
	}

	ratings := make([]*Rating, 0, len(rows))
	for _, r := range rows {
		ratings = append(ratings, ratingFromRow(sqlc.RatingRow(r)))
	}

	return ratings, nil
}
