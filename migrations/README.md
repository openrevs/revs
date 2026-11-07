migrate create -seq -digits 3 -dir migrations -ext sql year_allow_null

migrate -source file://migrations -database sqlite3://tmp2/dmrs.db up
