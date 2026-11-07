INSERT INTO users (login,email,userid,publickey,privatekey) VALUES ('admin',NULL,
		'8f16b43a63912d0c4f93b5cd3d5fa3c4','\x513bc85aab573f8b8abe64c4ec67eb6c5ab2d0ba9d2f321b62ae5c88e35130da','\x656c6c4199fafd141753062bd130f99881ef8f9629f13f6ad0d4ddb337cf15f2513bc85aab573f8b8abe64c4ec67eb6c5ab2d0ba9d2f321b62ae5c88e35130da');
INSERT INTO "titles" (name, year) VALUES ('Pulp Fiction', 1995);
INSERT INTO "titles" (name, year) VALUES ('Star Wars', 1979);

INSERT INTO "ratings" (user_id, title_id, rating, date) VALUES (1, 1, 5,CURRENT_TIMESTAMP);
INSERT INTO "ratings" (user_id, title_id, rating, date, comment) VALUES (1, 2, 4, CURRENT_TIMESTAMP, 'Far out');
