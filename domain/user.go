package domain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/kalafut/dmrs/sqlc"
)


func (u *DAL) UserRatings(ctx context.Context, id int) ([]*Rating, error) {
	rows, err := u.q.UserRatings(ctx, id)
	if err != nil {
		return nil, err
	}

	ratings := make([]*Rating, 0, len(rows))
	for _, r := range rows {
		ratings = append(ratings, ratingFromRow(sqlc.RatingRow(r)))
	}

	return ratings, nil
}

func (u *DAL) User(ctx context.Context, id int) (*User, error) {
	user, err := u.q.User(ctx, id)
	if err != nil {
		return nil, err
	}

	return userFromSqlc(user), nil
}

func (u *DAL) AddUser(ctx context.Context, user *User) error {
	return u.q.AddUser(ctx, sqlc.AddUserParams{
		Login:       user.Login,
		Password:    user.Password,
		Email:       user.Email,
		Userid:      user.Userid,
		Publickey:   user.Publickey,
		Privatekey:  user.Privatekey,
		Minrating:   user.Minrating,
		Maxrating:   user.Maxrating,
		Showposters: user.ShowPosters,
	})
}

func (u *DAL) UpdateUser(ctx context.Context, user *User) error {
	return u.q.UpdateUser(ctx, sqlc.UpdateUserParams{
		Login:       user.Login,
		Password:    user.Password,
		Email:       user.Email,
		Userid:      user.Userid,
		Publickey:   user.Publickey,
		Privatekey:  user.Privatekey,
		Minrating:   user.Minrating,
		Maxrating:   user.Maxrating,
		Revision:    user.Revision,
		Showposters: user.ShowPosters,
		UserID:      user.ID,
	})
}

func (u *DAL) LoginUser(ctx context.Context, login, password string) (*User, error) {
	row, err := u.q.UserByLogin(ctx, login)
	if err != nil {
		return nil, err
	}

	user := userFromSqlc(row)
	if !user.VerifyPassword(password) {
		return nil, InvalidPassword
	}

	return user, nil
}

func (u *DAL) AddRating(ctx context.Context, rating *Rating) error {
	if rating.ID == nil {
		return u.q.InsertRating(ctx, sqlc.InsertRatingParams{
			TitleID: rating.TitleID,
			UserID:  rating.UserID,
			Rating:  rating.Rating,
			Comment: rating.Comment,
			Updated: rating.Updated,
		})
	}

	return u.UpdateRating(ctx, rating)
}

func (u *DAL) UpdateRating(ctx context.Context, rating *Rating) error {
	if rating.ID == nil {
		return errors.New("cannot update a rating without an ID")
	}

	return u.q.UpdateRating(ctx, sqlc.UpdateRatingParams{
		TitleID:  rating.TitleID,
		UserID:   rating.UserID,
		Rating:   rating.Rating,
		Comment:  rating.Comment,
		Updated:  rating.Updated,
		RatingID: *rating.ID,
	})
}

func (u *DAL) Rating(ctx context.Context, id int) (*Rating, error) {
	row, err := u.q.Rating(ctx, id)
	if err != nil {
		return nil, err
	}

	return ratingFromRow(row), nil
}

func (u *DAL) DeleteRating(ctx context.Context, id int) error {
	return u.q.DeleteRating(ctx, id)
}

func (u *DAL) AppData(ctx context.Context, key string, value interface{}) error {
	valueRaw, err := u.q.AppData(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	return json.Unmarshal([]byte(valueRaw), value)
}

func (u *DAL) SetAppData(ctx context.Context, key string, value interface{}) error {
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return err
	}

	return u.q.SetAppData(ctx, sqlc.SetAppDataParams{
		Key:   key,
		Value: string(valueJSON),
	})
}
