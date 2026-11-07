-- Client tables
CREATE TABLE IF NOT EXISTS "users"
(
    "id" integer primary key autoincrement NOT NULL
    , "login" text UNIQUE NOT NULL
    , "password" blob NOT NULL
    , "email" text
    , "userid" text NOT NULL
    , "publickey" blob
    , "privatekey" blob
    , "minrating" integer NOT NULL DEFAULT 0
    , "maxrating" integer NOT NULL DEFAULT 5
    , "revision" integer NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS "titles"
(
    "id" integer primary key autoincrement NOT NULL
    , "type" varchar(255) NOT NULL DEFAULT ""
    , "name" varchar(255) NOT NULL
    , "year" integer
    , "tmdb_id" INTEGER
    , "tmdb_poster_url" TEXT
);

CREATE TABLE IF NOT EXISTS "ratings"
(
    "id" integer primary key autoincrement NOT NULL,
    "title_id" integer NOT NULL,
    "user_id" integer NOT NULL,
    "rating" integer,
    "comment" text,
    "date" datetime,
    FOREIGN KEY(title_id) REFERENCES titles(id),
    FOREIGN KEY(user_id) REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS "ratingsets"
(
    "id" integer primary key autoincrement NOT NULL
    , "userid" text NOT NULL
    , "revision" int NOT NULL
    , "source" text
    , "format" text
    , "data" blob NOT NULL
    , "refid" text NOT NULL
    , "status" text NOT NULL
    , "receivedtime" datetime NOT NULL
);

-- Server tables
CREATE TABLE IF NOT EXISTS "uploads"
(
    "id" integer primary key autoincrement NOT NULL
    , "refid" text NOT NULL
    , "uploadtime" datetime NOT NULL
    , "location" text NOT NULL
    , "filename" text NOT NULL
    , "size" int NOT NULL
    , "status" text NOT NULL
);

CREATE TABLE IF NOT EXISTS "snapshots"
(
    "id" integer primary key autoincrement NOT NULL
    , "revision" int NOT NULL
    , "createtime" datetime NOT NULL
    , "storelocation" text NOT NULL
    , "publishlocation" text NOT NULL
    , "size" bigint NOT NULL
    , "status" text NOT NULL
);

CREATE TABLE IF NOT EXISTS "reviews"
(
    "id" integer primary key autoincrement NOT NULL
    , "userid" string NOT NULL
    , "revision" int NOT NULL
    , "location" text NOT NULL
    , "size" int NOT NULL
    , "receivedtime" datetime NOT NULL
);

-- Already created: "ratingsets"
