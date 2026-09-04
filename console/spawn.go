package console

import "regexp"

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
