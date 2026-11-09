.DEFAULT_GOAL := build_sqlite

build_psql: models/boil_types_psql.go
	go build -tags psql -o bin/revs ./cmd/revs

#build_sqlite: models/boil_types_sqlite.go

build_sqlite: 
	go build -o bin/revs ./cmd/revs

generate:
	sqlc generate

test:
	go test ./... -count=1

models/boil_types_psql.go: sqlboiler.toml schemapg.sql
	#rm -f sample_client.db
	#sqlite3 sample_client.db < schema.sql
	psql revs -f schemapg.sql
	#sqlboiler sqlite3 --wipe -c sqlboiler.toml
	sqlboiler psql --wipe -c sqlboiler.toml
	mv models/boil_types.go models/boil_types_psql.go
	#rm -f sample_client.db

models/boil_types_sqlite.go: sqlboiler.toml schemapg.sql
	rm -f sample_client.db
	sqlite3 sample_client.db < schema.sql
	sqlboiler sqlite3 --wipe -c sqlboiler.toml
	mv models/boil_types.go models/boil_types_sqlite.go
	rm -f sample_client.db

testdb:
	mkdir -p tmp
	rm -f tmp/revs.db
	sqlite3 tmp/revs.db < schema.sql
	#sqlite3 tmp/revs.db < demo.sql

clean:
	rm -rf bin/
	rm -rf tmp/
