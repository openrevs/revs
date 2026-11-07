-- +goose Up

CREATE TABLE IF NOT EXISTS users
(
    user_id integer primary key NOT NULL
    , login text UNIQUE NOT NULL
    , password blob NOT NULL
    , email text
    , userid text NOT NULL
    , publickey blob
    , privatekey blob
    , minrating integer NOT NULL DEFAULT 0
    , maxrating integer NOT NULL DEFAULT 5
    , revision integer NOT NULL DEFAULT 0
, showposters boolean NOT NULL DEFAULT true);

CREATE TABLE IF NOT EXISTS titles
(
    title_id integer primary key NOT NULL
    , type varchar(255) NOT NULL
    , name varchar(255) NOT NULL
    , year INTEGER NOT NULL
    , tmdb_id INTEGER NOT NULL
    , tmdb_poster_url TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ratings
(
    rating_id integer primary key autoincrement NOT NULL,
    title_id integer NOT NULL,
    user_id integer NOT NULL,
    rating integer,
    comment text,
    date datetime, updated datetime not null DEFAULT '1970-01-01',
    FOREIGN KEY(title_id) REFERENCES titles(title_id),
    FOREIGN KEY(user_id) REFERENCES users(user_id)
);
CREATE TABLE IF NOT EXISTS ratingsets
(
    ratingset_id integer primary key autoincrement NOT NULL
    , userid text NOT NULL
    , revision int NOT NULL
    , source text
    , format text
    , data blob NOT NULL
    , refid text NOT NULL
    , status text NOT NULL
    , receivedtime datetime NOT NULL
);
CREATE TABLE IF NOT EXISTS uploads
(
    upload_id integer primary key autoincrement NOT NULL
    , refid text NOT NULL
    , uploadtime datetime NOT NULL
    , location text NOT NULL
    , filename text NOT NULL
    , size int NOT NULL
    , status text NOT NULL
);
CREATE TABLE IF NOT EXISTS snapshots
(
    snapshot_id integer primary key NOT NULL
    , revision int NOT NULL
    , createtime datetime NOT NULL
    , storelocation text NOT NULL
    , publishlocation text NOT NULL
    , size bigint NOT NULL
    , status text NOT NULL
);
CREATE TABLE IF NOT EXISTS reviews
(
    review_id integer primary key autoincrement NOT NULL
    , userid text NOT NULL
    , revision int NOT NULL
    , location text NOT NULL
    , size int NOT NULL
    , receivedtime datetime NOT NULL
);
CREATE TABLE IF NOT EXISTS app_data
(
    key TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL
);

-- +goose Down
DROP TABLE app_data;
DROP TABLE reviews;
DROP TABLE snapshots;
DROP TABLE uploads;
DROP TABLE ratingsets;
DROP TABLE ratings;
DROP TABLE titles;
DROP TABLE app_data;

