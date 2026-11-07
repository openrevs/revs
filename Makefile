.DEFAULT_GOAL := build_sqlite

build_psql: models/boil_types_psql.go
	go build -tags psql -o bin/dmrs ./cmd/dmrs

#build_sqlite: models/boil_types_sqlite.go

build_sqlite: 
	go build -o bin/dmrs ./cmd/dmrs

test:
	go test ./... -count=1

models/boil_types_psql.go: sqlboiler.toml schemapg.sql
	#rm -f sample_client.db
	#sqlite3 sample_client.db < schema.sql
	psql dmrs -f schemapg.sql
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
	rm -f tmp/dmrs.db
	sqlite3 tmp/dmrs.db < schema.sql
	#sqlite3 tmp/dmrs.db < demo.sql

clean:
	rm -rf bin/
	rm -rf tmp/
