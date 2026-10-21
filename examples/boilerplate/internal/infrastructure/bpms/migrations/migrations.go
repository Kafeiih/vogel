package migrations

import "embed"

const TableName = "bpms_db_version"

//go:embed *.sql
var FS embed.FS
