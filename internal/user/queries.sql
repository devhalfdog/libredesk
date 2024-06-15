-- name: get-users
SELECT first_name, last_name, uuid, disabled from users;

-- name: get-user-by-email
select id, email, password, avatar_url, first_name, last_name, uuid from users where email = $1;

-- name: get-user
select id, email, avatar_url, first_name, last_name, uuid from users where CASE WHEN $1 > 0 THEN id = $1 ELSE uuid = $2 END;

-- name: set-user-password
update users set password = $1 where id = $2;

-- name: get-inbox-id
select inbox_id from conversations where uuid = $1;