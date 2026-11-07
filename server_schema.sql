CREATE TABLE IF NOT EXISTS "uploads" (
    "id" integer primary key autoincrement NOT NULL
    , "refid" text NOT NULL
    , "uploadtime" datetime NOT NULL
    , "location" text NOT NULL
    , "filename" text NOT NULL
    , "size" int NOT NULL
    , "status" text NOT NULL
);

CREATE TABLE IF NOT EXISTS "snapshots" (
    "id" integer primary key autoincrement NOT NULL
	, "revision" int NOT NULL
    , "createtime" datetime NOT NULL
    , "storelocation" text NOT NULL
    , "publishlocation" text NOT NULL
    , "size" int NOT NULL
    , "status" text NOT NULL
);

CREATE TABLE IF NOT EXISTS "reviews" (
    "id" integer primary key autoincrement NOT NULL
	, "userid" string NOT NULL
	, "revision" int NOT NULL
    , "location" text NOT NULL
    , "size" int NOT NULL
    , "receivedtime" datetime NOT NULL
);

CREATE TABLE IF NOT EXISTS "ratingsets" (
    "id" integer primary key autoincrement NOT NULL
	, "userid" text NOT NULL
	, "revision" int NOT NULL
	, "source" text NOT NULL
	, "format" text NOT NULL
	, "data" blob NOT NULL
	, "refid" text NOT NULL
	, "status" text NOT NULL
    , "receivedtime" datetime NOT NULL
	);
