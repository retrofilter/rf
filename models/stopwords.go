package models

// Stopwords is Lucene's classic English stopword set (StandardAnalyzer /
// EnglishAnalyzer default): frequent enough to say nothing about relevance,
// short enough to enumerate.
var Stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "but": true, "by": true, "for": true, "if": true, "in": true,
	"into": true, "is": true, "it": true, "no": true, "not": true, "of": true,
	"on": true, "or": true, "such": true, "that": true, "the": true,
	"their": true, "then": true, "there": true, "these": true, "they": true,
	"this": true, "to": true, "was": true, "will": true, "with": true,
}
