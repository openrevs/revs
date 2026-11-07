-- name: User :one
SELECT * FROM users WHERE user_id = ?;

-- name: UserByLogin :one
SELECT * FROM users WHERE login = ?;

-- name: AddUser :exec
INSERT INTO users (login, password, email, userid, publickey, privatekey, minrating, maxrating, showposters)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateUser :exec
UPDATE users
SET login = ?, password = ?, email = ?, userid = ?, publickey = ?, privatekey = ?,
    minrating = ?, maxrating = ?, revision = ?, showposters = ?
WHERE user_id = ?;
