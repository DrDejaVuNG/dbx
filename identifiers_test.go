package dbx

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQuoteIdentifiers(t *testing.T) {
	tests := []struct {
		name, sql, expected string
	}{
		{"mixed-case names", `SELECT count(*) FROM blogPosts WHERE userId = $1 AND isPublished = true`,
			`SELECT count(*) FROM "blogPosts" WHERE "userId" = $1 AND "isPublished" = true`},
		{"lower and upper case names", `SELECT userid, SKU, id FROM posts`, `SELECT userid, SKU, id FROM posts`},
		{"expression in parentheses", `("likeCount" + 1) DESC, (likeCount + 1) DESC`,
			`("likeCount" + 1) DESC, ("likeCount" + 1) DESC`},
		{"aliases and CTE", `WITH recentPosts AS (SELECT userId AS ownerId FROM t) SELECT COUNT(*) AS totalCount FROM recentPosts`,
			`WITH "recentPosts" AS (SELECT "userId" AS "ownerId" FROM t) SELECT COUNT(*) AS "totalCount" FROM "recentPosts"`},
		{"qualified names", `SELECT p.authorId, "p".authorId, myPosts.id FROM posts myPosts`,
			`SELECT p."authorId", "p"."authorId", "myPosts".id FROM posts "myPosts"`},
		{"mixed-case keywords", `Select id From t Where a Is Not Null And b In (1) Order By c Desc Nulls Last Limit 1`,
			`Select id From t Where a Is Not Null And b In (1) Order By c Desc Nulls Last Limit 1`},
		{"keyword after a dot", `SELECT t.Name FROM t`, `SELECT t.Name FROM t`},
		{"function names", `SELECT Lower(userId), Coalesce(likeCount, 0), myFunc (x), pg_catalog.Lower(fooBar) FROM t`,
			`SELECT Lower("userId"), Coalesce("likeCount", 0), myFunc (x), pg_catalog.Lower("fooBar") FROM t`},
		{"string literals", `SELECT 'userId', 'it''s userId', E'it\'s userId', N'userId', U&'userId', B'01', X'1F' FROM t`,
			`SELECT 'userId', 'it''s userId', E'it\'s userId', N'userId', U&'userId', B'01', X'1F' FROM t`},
		{"dollar literals and params", `SELECT $$userId$$, $tag$isPublished $1$tag$, $1, $12 FROM t WHERE fooBar = $2`,
			`SELECT $$userId$$, $tag$isPublished $1$tag$, $1, $12 FROM t WHERE "fooBar" = $2`},
		{"quoted names", `SELECT "userId", "we""Ird", U&"fooBar" FROM "blogPosts"`, `SELECT "userId", "we""Ird", U&"fooBar" FROM "blogPosts"`},
		{"comments", "SELECT 1 -- userId\n/* userId /* nested isPublished */ fooBar */ FROM t WHERE fooBar = 1",
			"SELECT 1 -- userId\n/* userId /* nested isPublished */ fooBar */ FROM t WHERE \"fooBar\" = 1"},
		{"numbers", `SELECT 1e-5, 0x1F, 1_000, .5, 1.5E+3 FROM t`, `SELECT 1e-5, 0x1F, 1_000, .5, 1.5E+3 FROM t`},
		{"type names", `SELECT id::Text, fooBar::Jsonb, CAST(fooBar AS Jsonb), CAST((SELECT a AS fooBar) AS Varchar(10)) FROM t`,
			`SELECT id::Text, "fooBar"::Jsonb, CAST("fooBar" AS Jsonb), CAST((SELECT a AS "fooBar") AS Varchar(10)) FROM t`},
		{"typed literal", `SELECT Jsonb '[]', fooBar FROM t`, `SELECT Jsonb '[]', "fooBar" FROM t`},
		{"collate", `SELECT id FROM t WHERE userId = $1 COLLATE NOCASE OR a = $2 COLLATE NoCase`,
			`SELECT id FROM t WHERE "userId" = $1 COLLATE NOCASE OR a = $2 COLLATE NoCase`},
		{"extract field", `SELECT EXTRACT(Epoch FROM createdAt) FROM t`, `SELECT EXTRACT(Epoch FROM "createdAt") FROM t`},
		{"relation before parentheses", `INSERT INTO myTable(fooBar) VALUES ($1); CREATE TABLE IF NOT EXISTS myTable (fooBar TEXT REFERENCES otherTable (id)); CREATE INDEX myIdx ON myTable (fooBar); CREATE VIEW myView (a) AS SELECT 1`,
			`INSERT INTO "myTable"("fooBar") VALUES ($1); CREATE TABLE IF NOT EXISTS "myTable" ("fooBar" TEXT REFERENCES "otherTable" (id)); CREATE INDEX "myIdx" ON "myTable" ("fooBar"); CREATE VIEW "myView" (a) AS SELECT 1`},
		{"CTE and alias column lists", `WITH RECURSIVE myCte (a) AS (SELECT 1) SELECT * FROM myCte, jsonb_each(x) AS myItems(k, v)`,
			`WITH RECURSIVE "myCte" (a) AS (SELECT 1) SELECT * FROM "myCte", jsonb_each(x) AS "myItems"(k, v)`},
		{"bare user", `SELECT id FROM t WHERE user = $1 OR USER = $2 OR User = $3`,
			`SELECT id FROM t WHERE "user" = $1 OR "user" = $2 OR "User" = $3`},
		{"user as a relation", `INSERT INTO user (id) VALUES ($1); SELECT user.id FROM user`,
			`INSERT INTO "user" (id) VALUES ($1); SELECT "user".id FROM "user"`},
		{"user not a column", `SELECT t.user, current_user, session_user, users, user_id, "user" FROM t; CREATE USER bob; ALTER USER bob; DROP USER bob; CREATE USER MAPPING FOR USER SERVER s`,
			`SELECT t.user, current_user, session_user, users, user_id, "user" FROM t; CREATE USER bob; ALTER USER bob; DROP USER bob; CREATE USER MAPPING FOR USER SERVER s`},
		{"non-ASCII names", `SELECT héllo, fooBär FROM t`, `SELECT héllo, "fooBär" FROM t`},
		{"unterminated tokens", `SELECT 'userId`, `SELECT 'userId`},
		{"empty", ``, ``},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expected, quoteIdentifiers(test.sql))
			// quoting is idempotent
			assert.Equal(t, test.expected, quoteIdentifiers(test.expected))
		})
	}
}

func TestQuoteIdentifiersNoChangeNoAlloc(t *testing.T) {
	sql := `SELECT "posts".* FROM "posts" WHERE ("posts"."authorId" = $1 AND title ILIKE $2) ORDER BY "created" DESC LIMIT 30`
	allocs := testing.AllocsPerRun(100, func() {
		if quoteIdentifiers(sql) != sql {
			t.Fatal("unexpected change")
		}
	})
	assert.Equal(t, float64(0), allocs)
}

func TestDB_processSQLQuoteIdentifiers(t *testing.T) {
	sql := `SELECT [[fooBar]], fooBar, {{myTable}}.id FROM {{myTable}} WHERE userId = {:userId} AND user = {:user}`

	db := NewFromDB(nil, "pgx")
	s, names := db.processSQL(sql)
	assert.Equal(t, `SELECT "fooBar", fooBar, "myTable".id FROM "myTable" WHERE userId = $1 AND user = $2`, s, "off by default")
	assert.Equal(t, []string{"userId", "user"}, names)

	db.QuoteIdentifiers = true
	s, names = db.processSQL(sql)
	assert.Equal(t, `SELECT "fooBar", "fooBar", "myTable".id FROM "myTable" WHERE "userId" = $1 AND "user" = $2`, s)
	assert.Equal(t, []string{"userId", "user"}, names)

	s, _ = db.Clone().processSQL(sql)
	assert.Equal(t, `SELECT "fooBar", "fooBar", "myTable".id FROM "myTable" WHERE "userId" = $1 AND "user" = $2`, s, "Clone keeps QuoteIdentifiers")
}
