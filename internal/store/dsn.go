package store

import "github.com/go-sql-driver/mysql"

type parsedRootDSN struct {
	raw    mysql.Config
	dbName string
}

func parseRootCfg(dsn string) (parsedRootDSN, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return parsedRootDSN{}, err
	}
	return parsedRootDSN{raw: *cfg, dbName: cfg.DBName}, nil
}

// String returns the DSN without a database name (for CREATE DATABASE).
func (p parsedRootDSN) String() string {
	c := p.raw
	c.DBName = ""
	return c.FormatDSN()
}
