DROP TABLE IF EXISTS ratings;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS titles;
DROP TABLE IF EXISTS ratingsets;
DROP TABLE IF EXISTS uploads;
DROP TABLE IF EXISTS snapshots;
DROP TABLE IF EXISTS reviews;

-- Client tables
CREATE TABLE "users" (
    "id" serial primary key
    , "login" text UNIQUE NOT NULL
    , "password" bytea NOT NULL
    , "email" text
    , "userid" text NOT NULL
    , "publickey" bytea
    , "privatekey" bytea
    , "minrating" integer NOT NULL DEFAULT 0
    , "maxrating" integer NOT NULL DEFAULT 5
    , "revision" integer NOT NULL DEFAULT 0
    );


CREATE TABLE "titles" (
      "id" SERIAL PRIMARY KEY
    , "type" TEXT NOT NULL DEFAULT ''
    , "name" TEXT NOT NULL
    , "year" INTEGER NOT NULL
    , "tmdb_id" INTEGER
    , "tmdb_poster_url" TEXT
    );

CREATE TABLE "ratings" (
    "id" serial primary key,
    "title_id" integer NOT NULL,
    "user_id" integer NOT NULL,
    "rating" integer,
    "comment" text,
    "date" timestamptz(0),
    FOREIGN KEY(title_id) REFERENCES titles(id),
    FOREIGN KEY(user_id) REFERENCES users(id)
    );

CREATE TABLE "ratingsets" (
    "id" serial primary key
    , "userid" text NOT NULL
    , "revision" int NOT NULL
    , "source" text
    , "format" text
    , "data" bytea NOT NULL
    , "refid" text NOT NULL
    , "status" text NOT NULL
    , "receivedtime" timestamptz(0) NOT NULL
    );

-- Server tables
CREATE TABLE "uploads" (
    "id" serial primary key
    , "refid" text NOT NULL
    , "uploadtime" timestamptz(0) NOT NULL
    , "location" text NOT NULL
    , "filename" text NOT NULL
    , "size" int NOT NULL
    , "status" text NOT NULL
);

CREATE TABLE "snapshots" (
    "id" serial primary key
    , "revision" int NOT NULL
    , "createtime" timestamptz(0) NOT NULL
    , "storelocation" text NOT NULL
    , "publishlocation" text NOT NULL
    , "size" bigint NOT NULL
    , "status" text NOT NULL
);

CREATE TABLE "reviews" (
    "id" serial primary key
    , "userid" text NOT NULL
    , "revision" int NOT NULL
    , "location" text NOT NULL
    , "size" int NOT NULL
    , "receivedtime" timestamptz(0) NOT NULL
);

-- Already created: "ratingsets"
