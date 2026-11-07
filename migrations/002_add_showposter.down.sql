CREATE TEMPORARY TABLE backup
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

INSERT INTO backup SELECT
                       "id", "login", "password" , "email" , "userid" , "publickey" , "privatekey" , "minrating",
                       "maxrating", "revision" FROM users;
DROP TABLE users;
CREATE TABLE users
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
INSERT INTO users SELECT
                      "id", "login", "password" , "email" , "userid" , "publickey" , "privatekey" , "minrating",
                      "maxrating", "revision" FROM backup;
DROP TABLE backup;
