package migrationlint

import "github.com/reliant-labs/forge/internal/sqllex"

// The statement lexer lives in internal/sqllex, which `forge db analyze`
// shares; these names keep the rules reading as they did.

type lexedStatement = sqllex.Statement

func lexStatements(src string) []lexedStatement { return sqllex.Split(src) }

var (
	blockCommentEnd = sqllex.BlockCommentEnd
	quotedEnd       = sqllex.QuotedEnd
	dollarQuoteEnd  = sqllex.DollarQuoteEnd
	isIdentStart    = sqllex.IsIdentStart
	isIdentByte     = sqllex.IsIdentByte
	identBefore     = sqllex.IdentBefore
)
